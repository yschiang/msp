package conformance

// This file: the live conformance checks C2–C6 (Probe, run from inside the
// model container's network namespace by `msp-conform probe`) and the static
// half of C4 (CheckSchemaStatic, run host-side by `verify` before the model
// starts). Everything here is pure gRPC + files — no docker — so the whole
// file is unit-tested against an internal/stub-based fake.

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"github.com/yschiang/msp/msp/internal/envelope"
	"github.com/yschiang/msp/msp/internal/manifest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// garbagePayload is C3(b)'s deliberately unparseable input: 0xDE opens field
// 27 with wire type 6, which protobuf does not define, so strict and lenient
// parsers alike must reject it (TestGarbagePayloadGenuinelyDoesNotParse proves
// it). The SDK returns INVALID_INPUT for exactly this class of payload.
var garbagePayload = []byte{0xDE, 0xAD, 0xBE, 0xEF}

const (
	healthPollInterval = 500 * time.Millisecond
	// rpcTimeout bounds each Predict so a hung model cannot hang conformance.
	// ponytail: one flat constant; make it manifest-driven if a real model
	// ever needs >30s per golden inference.
	rpcTimeout    = 30 * time.Second
	probeDeviceID = "conform-probe"
)

// ProbeConfig carries everything the live checks need. DescDir and GoldenDir
// hold the files extracted from the image, flat under their container
// basenames (the envelope.LoadGoldens/Compare convention).
// ModelStarted is when the model container was started, which is when C2's
// startupSeconds budget begins. The probe runs in its own container, so it
// cannot observe that instant; the host passes it in. Zero means "now",
// for direct callers (tests) that start the server themselves.
type ProbeConfig struct {
	Target         string
	Manifest       *manifest.Manifest
	GoldenDir      string
	DescDir        string
	StartupSeconds int
	ModelStarted   time.Time
}

// Probe runs C2–C6 against a live model and returns their verdicts. Checks
// blocked by an earlier failure (no ready model, no golden samples) are
// absent from the report rather than guessed at. The C2 clock starts at
// cfg.ModelStarted, not at probe start: `docker run` of the probe container
// measures 300-550ms locally, and a clock started here would hand every model
// that much startup budget it did not earn.
func Probe(ctx context.Context, cfg ProbeConfig) *Report {
	r := &Report{}
	defer r.Aggregate()

	c2, client := checkStartup(ctx, cfg)
	r.Checks = append(r.Checks, c2)
	if !c2.Pass {
		return r
	}
	defer client.Close()

	pairs, err := envelope.LoadGoldens(cfg.Manifest, cfg.GoldenDir)
	if err == nil && len(pairs) == 0 {
		err = fmt.Errorf("manifest declares no golden samples")
	}
	if err != nil {
		// C5 owns golden-sample availability; C3, C4, C6 also need
		// sample-01 and cannot run.
		r.Checks = append(r.Checks, CheckResult{
			ID: "C5", Name: CheckNames["C5"],
			Detail: fmt.Sprintf("golden samples unavailable: %v", err),
		})
		return r
	}

	c3, c4 := checkEnvelope(ctx, cfg, pairs[0])
	c5 := checkGoldens(ctx, cfg, client, pairs)
	c6 := checkIdempotency(ctx, cfg, client, pairs[0])
	r.Checks = append(r.Checks, c3, c4, c5, c6)
	return r
}

// checkStartup is C2: dial the model and poll Health every 500ms until ready
// or the startupSeconds deadline. On success it hands the connected client to
// the later checks; on failure it returns nil and the caller stops.
func checkStartup(ctx context.Context, cfg ProbeConfig) (CheckResult, *envelope.Client) {
	res := CheckResult{ID: "C2", Name: CheckNames["C2"]}
	budget := time.Duration(cfg.StartupSeconds) * time.Second
	start := cfg.ModelStarted
	if start.IsZero() {
		start = time.Now()
	}
	deadline := start.Add(budget)

	// The probe's own container startup is spent from the model's budget, so
	// it can in principle be gone already.
	if remaining := time.Until(deadline); remaining <= 0 {
		res.Detail = fmt.Sprintf("the %v startup budget was already spent before the probe could connect (%v elapsed since the model container started)",
			budget, time.Since(start).Round(time.Millisecond))
		return res, nil
	}

	client, err := envelope.Dial(cfg.Target, time.Until(deadline))
	if err != nil {
		res.Detail = fmt.Sprintf("no connection to %s within %v startup budget: %v", cfg.Target, budget, err)
		return res, nil
	}

	last := "no health response yet"
	for {
		hctx, cancel := context.WithDeadline(ctx, deadline)
		h, err := client.Health(hctx)
		cancel()
		switch {
		case err == nil && h.Ready:
			res.Pass = true
			res.Detail = fmt.Sprintf("ready after %v (budget %v)", time.Since(start).Round(time.Millisecond), budget)
			return res, client
		case err != nil:
			last = fmt.Sprintf("health error: %v", err)
		default:
			last = fmt.Sprintf("ready=false detail=%q", h.Detail)
		}
		if ctx.Err() != nil || !time.Now().Add(healthPollInterval).Before(deadline) {
			client.Close()
			res.Detail = fmt.Sprintf("not ready within %v startup budget; last health: %s", budget, last)
			return res, nil
		}
		time.Sleep(healthPollInterval)
	}
}

// checkEnvelope is C3 plus the live half of C4. It uses its own raw gRPC
// client rather than envelope.Client because the request_id echo assertion
// needs to know the id that was sent, and envelope.Client generates its id
// internally.
func checkEnvelope(ctx context.Context, cfg ProbeConfig, golden envelope.GoldenPair) (c3, c4 CheckResult) {
	c3 = CheckResult{ID: "C3", Name: CheckNames["C3"]}
	c4 = CheckResult{ID: "C4", Name: CheckNames["C4"]}
	m := cfg.Manifest

	conn, err := grpc.NewClient(cfg.Target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		c3.Detail = fmt.Sprintf("dial: %v", err)
		c4.Detail = "no golden response to validate (C3 could not dial)"
		return c3, c4
	}
	defer conn.Close()
	svc := servingv1.NewModelServiceClient(conn)

	// (a) A golden input must come back OK with the envelope echoed.
	reqID := randomID()
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	resp, err := svc.Predict(rctx, &servingv1.PredictRequest{
		RequestId:  reqID,
		DeviceId:   probeDeviceID,
		ModelName:  m.Model.Name,
		IngestTime: timestamppb.Now(),
		Payload:    golden.Input,
	})
	cancel()
	var problems []string
	if err != nil {
		problems = append(problems, fmt.Sprintf("golden predict: %v", err))
	} else {
		if resp.Status != servingv1.Status_OK {
			problems = append(problems, fmt.Sprintf("golden input returned status %v, want OK", resp.Status))
		}
		if resp.RequestId != reqID {
			problems = append(problems, fmt.Sprintf("request_id %q not echoed (sent %q)", resp.RequestId, reqID))
		}
		if resp.ModelName != m.Model.Name {
			problems = append(problems, fmt.Sprintf("model_name %q, manifest declares %q", resp.ModelName, m.Model.Name))
		}
		if resp.ModelVersion != m.Model.Version {
			problems = append(problems, fmt.Sprintf("model_version %q, manifest declares %q", resp.ModelVersion, m.Model.Version))
		}
	}

	// (b) A payload that cannot parse must come back INVALID_INPUT — as
	// response data, not a transport error.
	gctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	gresp, gerr := svc.Predict(gctx, &servingv1.PredictRequest{
		RequestId:  randomID(),
		DeviceId:   probeDeviceID,
		ModelName:  m.Model.Name,
		IngestTime: timestamppb.Now(),
		Payload:    garbagePayload,
	})
	cancel()
	switch {
	case gerr != nil:
		problems = append(problems, fmt.Sprintf("garbage predict: %v (want INVALID_INPUT as response data)", gerr))
	case gresp.Status != servingv1.Status_INVALID_INPUT:
		problems = append(problems, fmt.Sprintf("garbage payload returned status %v, want INVALID_INPUT", gresp.Status))
	}

	if len(problems) == 0 {
		c3.Pass = true
		c3.Detail = "golden input -> OK with envelope echoed; garbage payload -> INVALID_INPUT"
	} else {
		c3.Detail = strings.Join(problems, "; ")
	}

	// C4 live half: the OK golden response payload must strict-parse as the
	// declared output message. An empty/absent payload from a non-OK response
	// would strict-parse as an all-defaults message, so only an OK response
	// counts.
	if err != nil || resp.Status != servingv1.Status_OK {
		c4.Detail = "no OK golden response to validate (see C3)"
		return c3, c4
	}
	md, mdErr := loadMessageDescriptor(cfg.DescDir, m.Contract.OutputSchema)
	if mdErr != nil {
		c4.Detail = mdErr.Error()
		return c3, c4
	}
	if perr := strictParse(resp.Payload, md); perr != nil {
		c4.Detail = fmt.Sprintf("golden response does not strict-parse as %s: %v", m.Contract.OutputSchema.MessageType, perr)
		return c3, c4
	}
	c4.Pass = true
	c4.Detail = fmt.Sprintf("golden response strict-parses as %s", m.Contract.OutputSchema.MessageType)
	return c3, c4
}

// checkGoldens is C5: every golden input must produce the expected output
// under the pair's tolerance. All samples are evaluated so the detail names
// every failing sample, not just the first.
func checkGoldens(ctx context.Context, cfg ProbeConfig, client *envelope.Client, pairs []envelope.GoldenPair) CheckResult {
	res := CheckResult{ID: "C5", Name: CheckNames["C5"]}
	m := cfg.Manifest
	var problems []string
	for i, p := range pairs {
		name := filepath.Base(m.GoldenSamples[i].Input)
		rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
		resp, err := client.Predict(rctx, m.Model.Name, probeDeviceID, p.Input)
		cancel()
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
		case resp.Status != servingv1.Status_OK:
			problems = append(problems, fmt.Sprintf("%s: status %v, want OK", name, resp.Status))
		default:
			if cerr := envelope.Compare(p.Expected, resp.Payload, p.Tolerance, m.Contract.OutputSchema, cfg.DescDir); cerr != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", name, cerr))
			}
		}
	}
	if len(problems) == 0 {
		res.Pass = true
		res.Detail = fmt.Sprintf("%d golden samples match expected", len(pairs))
	} else {
		res.Detail = strings.Join(problems, "; ")
	}
	return res
}

// checkIdempotency is C6: golden sample-01 sent three times on one
// connection; the outputs must agree pairwise under the manifest's
// comparisonPolicy (not the sample tolerance — the policy is what production
// paired diffs use). Self-consistency only: C5 already compared against
// expected.
func checkIdempotency(ctx context.Context, cfg ProbeConfig, client *envelope.Client, golden envelope.GoldenPair) CheckResult {
	res := CheckResult{ID: "C6", Name: CheckNames["C6"]}
	m := cfg.Manifest
	var outs [3][]byte
	for i := range outs {
		rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
		resp, err := client.Predict(rctx, m.Model.Name, probeDeviceID, golden.Input)
		cancel()
		if err != nil {
			res.Detail = fmt.Sprintf("call %d: %v", i+1, err)
			return res
		}
		if resp.Status != servingv1.Status_OK {
			res.Detail = fmt.Sprintf("call %d: status %v, want OK", i+1, resp.Status)
			return res
		}
		outs[i] = resp.Payload
	}
	for _, pair := range [][2]int{{0, 1}, {0, 2}, {1, 2}} {
		if err := envelope.Compare(outs[pair[0]], outs[pair[1]], m.ComparisonPolicy, m.Contract.OutputSchema, cfg.DescDir); err != nil {
			res.Detail = fmt.Sprintf("calls %d and %d differ under policy %q: %v", pair[0]+1, pair[1]+1, m.ComparisonPolicy, err)
			return res
		}
	}
	res.Pass = true
	res.Detail = fmt.Sprintf("3 calls agree under policy %q", m.ComparisonPolicy)
	return res
}

// CheckSchemaStatic is the host-side half of C4: the declared descriptors
// exist and contain the declared message types, every golden file
// strict-parses as its declared type, and the contract features the reference
// build deliberately does not implement (jsonschema payloads, top-k
// comparison) are rejected here — the manifest schema accepts them, so this
// gate is where they must stop.
func CheckSchemaStatic(m *manifest.Manifest, descDir, goldenDir string) CheckResult {
	res := CheckResult{ID: "C4", Name: CheckNames["C4"]}

	policies := []string{m.ComparisonPolicy}
	for _, gs := range m.GoldenSamples {
		policies = append(policies, gs.Tolerance)
	}
	for _, p := range policies {
		if strings.HasPrefix(p, "top-k:") {
			res.Detail = fmt.Sprintf("comparison policy %q: not implemented in reference build", p)
			return res
		}
	}

	sides := []struct {
		name   string
		schema manifest.SchemaRef
	}{{"input", m.Contract.InputSchema}, {"output", m.Contract.OutputSchema}}
	mds := map[string]protoreflect.MessageDescriptor{}
	for _, s := range sides {
		md, err := loadMessageDescriptor(descDir, s.schema)
		if err != nil {
			res.Detail = fmt.Sprintf("%s schema: %v", s.name, err)
			return res
		}
		mds[s.name] = md
	}

	for _, gs := range m.GoldenSamples {
		for _, f := range []struct {
			side, path string
		}{{"input", gs.Input}, {"output", gs.Output}} {
			raw, err := os.ReadFile(filepath.Join(goldenDir, filepath.Base(f.path)))
			if err != nil {
				res.Detail = fmt.Sprintf("golden %s: %v", f.side, err)
				return res
			}
			if err := strictParse(raw, mds[f.side]); err != nil {
				res.Detail = fmt.Sprintf("golden %s %s does not strict-parse as %s: %v",
					f.side, filepath.Base(f.path), mds[f.side].FullName(), err)
				return res
			}
		}
	}

	res.Pass = true
	res.Detail = fmt.Sprintf("descriptors declare %s / %s; %d golden pairs strict-parse",
		m.Contract.InputSchema.MessageType, m.Contract.OutputSchema.MessageType, len(m.GoldenSamples))
	return res
}

// loadMessageDescriptor resolves a manifest SchemaRef to a message descriptor.
// The descriptor path is an absolute container path, resolved like every
// extracted file: filepath.Join(descDir, filepath.Base(path)). Non-protobuf
// schema types are rejected here — the reference build implements only
// protobuf payload schemas.
func loadMessageDescriptor(descDir string, ref manifest.SchemaRef) (protoreflect.MessageDescriptor, error) {
	if ref.Type != "protobuf" {
		return nil, fmt.Errorf("schema type %q: not implemented in reference build", ref.Type)
	}
	path := filepath.Join(descDir, filepath.Base(ref.Descriptor))
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("descriptor %s (declared %s): %w", path, ref.Descriptor, err)
	}
	var fdset descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(raw, &fdset); err != nil {
		return nil, fmt.Errorf("descriptor %s: %w", path, err)
	}
	files, err := protodesc.NewFiles(&fdset)
	if err != nil {
		return nil, fmt.Errorf("descriptor %s: %w", path, err)
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName(ref.MessageType))
	if err != nil {
		return nil, fmt.Errorf("message %q not found in %s: %w", ref.MessageType, path, err)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("%q in %s is a %T, not a message", ref.MessageType, path, d)
	}
	return md, nil
}

// strictParse accepts a payload only if it (1) parses as md, (2) carries no
// unknown fields anywhere in the message tree, and (3) re-serializes to the
// identical bytes. (2) is load-bearing: a payload of a wire-compatible but
// different type decodes into unknown fields, which re-serialize back to the
// same bytes — byte-equality alone would rubber-stamp it.
func strictParse(payload []byte, md protoreflect.MessageDescriptor) error {
	msg := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(payload, msg); err != nil {
		return err
	}
	if err := checkNoUnknown("", msg); err != nil {
		return err
	}
	re, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return fmt.Errorf("re-serialize: %w", err)
	}
	if !bytes.Equal(re, payload) {
		return fmt.Errorf("payload does not re-serialize identically (%d bytes in, %d bytes out): non-canonical encoding",
			len(payload), len(re))
	}
	return nil
}

// checkNoUnknown walks the message tree and errors on the first unknown
// fields found, naming where.
func checkNoUnknown(path string, msg protoreflect.Message) error {
	if u := msg.GetUnknown(); len(u) > 0 {
		where := path
		if where == "" {
			where = string(msg.Descriptor().Name())
		}
		return fmt.Errorf("%s carries %d bytes of unknown fields", where, len(u))
	}
	var walkErr error
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		sub := func(p string, m protoreflect.Message) bool {
			if err := checkNoUnknown(p, m); err != nil {
				walkErr = err
				return false
			}
			return true
		}
		name := path + "." + string(fd.Name())
		if path == "" {
			name = string(fd.Name())
		}
		switch {
		case fd.IsMap():
			if fd.MapValue().Kind() != protoreflect.MessageKind {
				return true
			}
			ok := true
			v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				ok = sub(fmt.Sprintf("%s[%s]", name, k), mv.Message())
				return ok
			})
			return ok
		case fd.IsList():
			if fd.Kind() != protoreflect.MessageKind {
				return true
			}
			for i := 0; i < v.List().Len(); i++ {
				if !sub(fmt.Sprintf("%s[%d]", name, i), v.List().Get(i).Message()) {
					return false
				}
			}
			return true
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			return sub(name, v.Message())
		default:
			return true
		}
	})
	return walkErr
}

// randomID returns a fresh probe request id. crypto/rand failure is
// practically impossible; fall back to a timestamp rather than plumbing an
// error through every check.
func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("probe-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("probe-%x", b)
}
