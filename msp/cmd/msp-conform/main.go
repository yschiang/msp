// Command msp-conform is the Phase 0 conformance gate (MSP-SPEC-001 §4.4):
// `msp-conform verify <image>` runs checks C1–C7 against a model image and
// exits 0 only if all seven pass. The hidden `probe` subcommand is what
// verify runs inside the model container's network namespace; it is not part
// of the operator surface. All behavior lives in internal/conformance; this
// file is flag parsing, wiring, output, and exit codes only.
//
// Exit codes: 0 all checks passed; 1 one or more checks failed or did not
// run; 2 usage or infrastructure error (docker unusable, bad flags).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yschiang/msp/msp/internal/conformance"
	"github.com/yschiang/msp/msp/internal/manifest"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var code int
	switch os.Args[1] {
	case "verify":
		code = runVerify(ctx, os.Args[2:])
	case "probe":
		code = runProbe(ctx, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "msp-conform: unknown command %q\n", os.Args[1])
		usage()
		code = 2
	}
	stop() // restore default signal handling before exiting
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: msp-conform verify [flags] <image>

Runs conformance checks C1-C7 against a model image. Exit 0 only if all pass.

Flags:
  --limits <yaml>  override resource ceilings (flat map: cpu, memory, nvidia.com/gpu)
  --json           print the report as JSON instead of the per-check table
  --keep           keep the extraction dir and containers for debugging`)
}

func runVerify(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	limits := fs.String("limits", "", "YAML file overriding resource ceilings")
	jsonOut := fs.Bool("json", false, "print the report as JSON")
	keep := fs.Bool("keep", false, "keep extraction dir and containers")
	// Accept the brief's `verify <image> [flags]` order as well as flags-first.
	var image string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		image, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if image == "" {
		image = fs.Arg(0)
	}
	if image == "" {
		fmt.Fprintln(os.Stderr, "msp-conform: verify needs an image")
		usage()
		return 2
	}

	opts := conformance.VerifyOptions{Keep: *keep}
	if *limits != "" {
		raw, err := os.ReadFile(*limits)
		if err != nil {
			fmt.Fprintf(os.Stderr, "msp-conform: %v\n", err)
			return 2
		}
		opts.Ceilings, err = conformance.LoadCeilings(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "msp-conform: %v\n", err)
			return 2
		}
	}

	report, err := conformance.VerifyImage(ctx, image, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "msp-conform: %v\n", err)
		return 2
	}
	printReport(report, *jsonOut)
	if report.Pass {
		return 0
	}
	return 1
}

// runProbe is the hidden subcommand verify executes inside the model
// container's network namespace. Descriptors are read from the manifest's own
// directory (extraction lays every declared descriptor flat next to it).
func runProbe(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	manifestPath := fs.String("manifest", "", "path to the extracted model-manifest.yaml")
	goldenDir := fs.String("golden-dir", "", "directory of extracted golden files")
	target := fs.String("target", "", "ModelService target, host:port")
	startupSeconds := fs.Int("startup-seconds", 0, "startup budget; 0 means the manifest's runtime.startupSeconds")
	jsonOut := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifestPath == "" || *goldenDir == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "msp-conform probe: --manifest, --golden-dir, and --target are required")
		return 2
	}
	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "msp-conform probe: %v\n", err)
		return 2
	}
	m, err := manifest.Load(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "msp-conform probe: %v\n", err)
		return 2
	}
	if *startupSeconds == 0 {
		*startupSeconds = m.Runtime.StartupSeconds
	}

	report := conformance.Probe(ctx, conformance.ProbeConfig{
		Target:         *target,
		Manifest:       m,
		GoldenDir:      *goldenDir,
		DescDir:        filepath.Dir(*manifestPath),
		StartupSeconds: *startupSeconds,
	})
	printReport(report, *jsonOut)
	if report.Pass {
		return 0
	}
	return 1
}

func printReport(r *conformance.Report, asJSON bool) {
	if asJSON {
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "msp-conform: marshal report: %v\n", err)
			return
		}
		fmt.Println(string(b))
		return
	}
	r.WriteHuman(os.Stdout)
}
