package conformance

// This file: the host-side orchestration behind `msp-conform verify` —
// extract manifest/goldens/descriptors from the image with docker create/cp,
// run C1/C7/C4-static locally, then start the model with zero network egress
// (--network none) and run the probe image inside the model's own network
// namespace (--network container:<id>, which is also how the probe reaches
// the model on macOS, where the host cannot reach container IPs). The docker
// CLI is exec'ed directly — deliberately no docker SDK dependency.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yschiang/msp/msp/internal/manifest"
)

// ProbeImage is the conformance probe image `make image-probe` builds.
const ProbeImage = "msp-conform-probe:dev"

// probeExtraBudget bounds the probe run beyond the model's startup budget.
// ponytail: flat 5-minute backstop over the per-RPC timeouts inside the
// probe; size it from the golden count if a model ever ships hundreds.
const probeExtraBudget = 5 * time.Minute

// VerifyOptions configures VerifyImage. A nil Ceilings means DefaultCeilings.
type VerifyOptions struct {
	Ceilings Ceilings
	Keep     bool
}

// VerifyImage runs the full C1–C7 gate against a model image and returns the
// report. The error return is for infrastructure failures only (docker or the
// probe image unusable, extraction dir not writable); a nonconformant image
// is a report with failing checks, not an error. Containers and the
// extraction directory are removed on every return path unless opts.Keep.
func VerifyImage(ctx context.Context, image string, opts VerifyOptions) (*Report, error) {
	if opts.Ceilings == nil {
		opts.Ceilings = DefaultCeilings
	}
	r := &Report{Image: image}
	// finalize: a verify passes only when all seven checks ran and passed.
	finalize := func() *Report {
		r.Aggregate()
		if len(r.MissingChecks()) > 0 {
			r.Pass = false
		}
		return r
	}

	runID := strings.TrimPrefix(randomID(), "probe-")
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cwd, ".msp-conform-tmp", runID)
	goldenDir := filepath.Join(dir, "golden")
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		return nil, fmt.Errorf("extraction dir: %w", err)
	}
	cl := &cleaner{keep: opts.Keep}
	defer cl.run()
	cl.dirs = append(cl.dirs, dir)

	// --- Extraction: docker create / cp / rm ---
	out, err := dockerRun(ctx, "create", image)
	if err != nil {
		return nil, fmt.Errorf("docker create %s: %w", image, err)
	}
	extractID := strings.TrimSpace(out)
	cl.containers = append(cl.containers, extractID)

	manifestHost := filepath.Join(dir, "model-manifest.yaml")
	if _, err := dockerRun(ctx, "cp", extractID+":/opt/msp/model-manifest.yaml", manifestHost); err != nil {
		r.Checks = append(r.Checks, CheckResult{ID: "C1", Name: CheckNames["C1"],
			Detail: fmt.Sprintf("/opt/msp/model-manifest.yaml not found in image: %v", err)})
		return finalize(), nil
	}
	raw, err := os.ReadFile(manifestHost)
	if err != nil {
		return nil, fmt.Errorf("read extracted manifest: %w", err)
	}

	// --- C1 (fatal on failure: every later check needs the manifest) ---
	m, loadErr := manifest.Load(raw)
	if loadErr != nil {
		r.Checks = append(r.Checks, CheckResult{ID: "C1", Name: CheckNames["C1"], Detail: loadErr.Error()})
		return finalize(), nil
	}
	r.Checks = append(r.Checks, CheckResult{ID: "C1", Name: CheckNames["C1"], Pass: true,
		Detail: fmt.Sprintf("manifest parsed and valid (model %s %s)", m.Model.Name, m.Model.Version)})

	// Goldens: `<cid>:/opt/msp/golden/.` copies the directory CONTENTS into
	// goldenDir, landing the .bin files flat under their container basenames
	// — the layout envelope.LoadGoldens resolves against. A failed cp (dir
	// absent from the image) is tolerated here; the probe's C5 then fails on
	// the missing files.
	if _, err := dockerRun(ctx, "cp", extractID+":/opt/msp/golden/.", goldenDir); err != nil {
		fmt.Fprintf(os.Stderr, "msp-conform: warning: could not extract goldens: %v\n", err)
	}

	// Declared protobuf descriptors, flat by basename next to the manifest
	// (the convention loadMessageDescriptor and envelope.Compare share).
	var c4Static CheckResult
	descByBase := map[string]string{}
	descOK := true
	for _, ref := range []manifest.SchemaRef{m.Contract.InputSchema, m.Contract.OutputSchema} {
		if ref.Type != "protobuf" || ref.Descriptor == "" {
			continue // non-protobuf schemas are rejected by C4-static
		}
		base := filepath.Base(ref.Descriptor)
		if prev, ok := descByBase[base]; ok {
			if prev != ref.Descriptor {
				c4Static = CheckResult{ID: "C4", Name: CheckNames["C4"], Detail: fmt.Sprintf(
					"descriptor basename collision: %s and %s both extract as %s", prev, ref.Descriptor, base)}
				descOK = false
			}
			continue
		}
		descByBase[base] = ref.Descriptor
		if _, err := dockerRun(ctx, "cp", extractID+":"+ref.Descriptor, filepath.Join(dir, base)); err != nil {
			fmt.Fprintf(os.Stderr, "msp-conform: warning: could not extract descriptor %s: %v\n", ref.Descriptor, err)
		}
	}
	// The create-only container has done its job; remove it eagerly so a
	// long probe phase doesn't leave it around (cleaner still backstops,
	// and under --keep it stays for inspection like everything else).
	if !opts.Keep {
		dockerRun(context.Background(), "rm", "-f", extractID) //nolint:errcheck
	}

	// --- C7 (host side) ---
	c7 := CheckResult{ID: "C7", Name: CheckNames["C7"], Pass: true, Detail: "resource declarations within ceilings"}
	if err := CheckResources(m.Runtime.Resources.Requests, m.Runtime.Resources.Limits, opts.Ceilings); err != nil {
		c7.Pass = false
		c7.Detail = err.Error()
	}

	// --- C4 static half (host side) ---
	if descOK {
		c4Static = CheckSchemaStatic(m, dir, goldenDir)
	}

	// --- C2–C6 live: model with zero egress, probe in the model's netns ---
	modelName := "msp-conform-model-" + runID
	probeName := "msp-conform-probe-" + runID
	cl.containers = append(cl.containers, modelName, probeName)

	liveChecks := map[string]CheckResult{}
	_, modelErr := dockerRun(ctx, "run", "-d", "--network", "none", "--name", modelName, image)
	// C2's startupSeconds budget runs from here. The probe cannot see this
	// instant from inside its own container, and `docker run` of that
	// container costs 300-550ms locally, so the host stamps it and passes it
	// down -- otherwise every model gets that much budget for free.
	modelStarted := time.Now()
	if modelErr != nil {
		liveChecks["C2"] = CheckResult{ID: "C2", Name: CheckNames["C2"],
			Detail: fmt.Sprintf("model container failed to start with --network none: %v", modelErr)}
	} else {
		budget := time.Duration(m.Runtime.StartupSeconds) * time.Second
		pctx, cancel := context.WithTimeout(ctx, time.Until(modelStarted.Add(budget))+probeExtraBudget)
		probeOut, probeErr := dockerRun(pctx, "run", "--rm", "--name", probeName,
			"--network", "container:"+modelName,
			"-v", dir+":/conform:ro",
			ProbeImage, "probe",
			"--manifest", "/conform/model-manifest.yaml",
			"--golden-dir", "/conform/golden",
			"--target", fmt.Sprintf("127.0.0.1:%d", m.Contract.Port),
			"--startup-seconds", fmt.Sprint(m.Runtime.StartupSeconds),
			"--model-started", modelStarted.Format(time.RFC3339Nano),
			"--json")
		cancel()

		var probeReport Report
		if jerr := json.Unmarshal([]byte(probeOut), &probeReport); jerr == nil && len(probeReport.Checks) > 0 {
			for _, c := range probeReport.Checks {
				liveChecks[c.ID] = c
			}
		} else {
			// The probe produced no report (model exited before the probe
			// could join its netns, probe image missing, timeout). Attribute
			// to C2 — the model was not verifiably up — with full context.
			liveChecks["C2"] = CheckResult{ID: "C2", Name: CheckNames["C2"],
				Detail: fmt.Sprintf("probe produced no report: %v; probe output: %s", probeErr, tail(probeOut, 400))}
		}
	}

	// Attach the model's log tail to the first failing live check: the probe
	// can't see docker logs from inside the netns, so the host adds them.
	for _, id := range []string{"C2", "C3", "C4", "C5", "C6"} {
		if c, ok := liveChecks[id]; ok && !c.Pass {
			if logs := modelLogsTail(modelName); logs != "" {
				c.Detail += "\nmodel logs (tail):\n" + logs
				liveChecks[id] = c
			}
			break
		}
	}

	// --- Assemble in spec order ---
	if c2, ok := liveChecks["C2"]; ok {
		r.Checks = append(r.Checks, c2)
	}
	if c3, ok := liveChecks["C3"]; ok {
		r.Checks = append(r.Checks, c3)
	}
	var c4Live *CheckResult
	if c4, ok := liveChecks["C4"]; ok {
		c4Live = &c4
	}
	if merged := mergeC4(c4Static, c4Live); merged != nil {
		r.Checks = append(r.Checks, *merged)
	}
	if c5, ok := liveChecks["C5"]; ok {
		r.Checks = append(r.Checks, c5)
	}
	if c6, ok := liveChecks["C6"]; ok {
		r.Checks = append(r.Checks, c6)
	}
	r.Checks = append(r.Checks, c7)
	return finalize(), nil
}

// mergeC4 combines C4's host-side static half with the probe's live half.
// The check passes only when both halves ran and passed; a failed static half
// fails C4 outright; a passing static half with no live verdict leaves C4
// not-run (nil) — half a verification is not a pass.
func mergeC4(static CheckResult, live *CheckResult) *CheckResult {
	if !static.Pass {
		merged := static
		if live != nil {
			merged.Detail += "; live: " + live.Detail
		}
		return &merged
	}
	if live == nil {
		return nil
	}
	merged := *live
	merged.Detail = static.Detail + "; " + live.Detail
	return &merged
}

// dockerRun executes `docker args...` and returns its stdout. On a non-zero
// exit the error carries the stderr tail; stdout is still returned (the probe
// exits non-zero when checks fail but its report JSON is on stdout).
func dockerRun(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("docker %s: %v: %s", args[0], err, tail(stderr.String(), 400))
	}
	return stdout.String(), nil
}

// modelLogsTail returns the last lines of the model container's output, or ""
// if unavailable.
func modelLogsTail(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "logs", "--tail", "20", name).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// cleaner removes the containers and directories a verify run creates. It
// runs in a defer, so every return path — success, check failure, infra
// error, panic, and ctx cancellation on SIGINT/SIGTERM — is covered; it uses
// fresh contexts because the run's ctx may already be cancelled. A leaked
// --network none container is invisible in normal docker ps habits, so
// removal is forced and by name.
type cleaner struct {
	keep       bool
	containers []string
	dirs       []string
}

func (c *cleaner) run() {
	if c.keep {
		fmt.Fprintf(os.Stderr, "msp-conform: --keep: leaving extraction dir %s and containers %s\n",
			strings.Join(c.dirs, ", "), strings.Join(c.containers, ", "))
		return
	}
	for _, name := range c.containers {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		// Most are already gone (--rm, eager rm); errors are expected noise.
		exec.CommandContext(ctx, "docker", "rm", "-f", name).Run() //nolint:errcheck
		cancel()
	}
	for _, d := range c.dirs {
		os.RemoveAll(d) //nolint:errcheck
	}
}
