package conformance

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	examplev1 "github.com/yschiang/msp/msp/gen/examplev1"
	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"github.com/yschiang/msp/msp/internal/manifest"
	"github.com/yschiang/msp/msp/internal/stub"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// Real committed example-model fixtures (Tasks 4/5), reused the same way the
// traffic tests reuse them.
const (
	exampleGoldenDir = "../../../contract/examples/defect-cls/golden"
	exampleDescDir   = "../../../contract/examples/defect-cls/schemas"
)

// exampleManifest builds the manifest the probe tests run against: the real
// example model's schema/golden declarations, with model version matching the
// stub's default so the happy path passes C3.
func exampleManifest() *manifest.Manifest {
	var m manifest.Manifest
	m.Model.Name = "defect-cls"
	m.Model.Version = stub.DefaultVersion
	m.Contract.Port = 8080
	m.Contract.InputSchema = manifest.SchemaRef{
		Type: "protobuf", Descriptor: "/opt/msp/schemas/input.desc", MessageType: "msp.example.v1.DefectInput"}
	m.Contract.OutputSchema = manifest.SchemaRef{
		Type: "protobuf", Descriptor: "/opt/msp/schemas/output.desc", MessageType: "msp.example.v1.DefectOutput"}
	m.ComparisonPolicy = "exact"
	m.GoldenSamples = []manifest.GoldenSample{
		{Input: "/opt/msp/golden/sample-01.bin", Output: "/opt/msp/golden/expected-01.bin"},
	}
	return &m
}

// validatingServer wraps the stub with the SDK's strict input validation: a
// payload that does not parse as DefectInput gets INVALID_INPUT as response
// data (never a gRPC error), which is what C3(b) asserts. notReady simulates
// a model stuck in load() for C2.
type validatingServer struct {
	servingv1.UnimplementedModelServiceServer
	inner    *stub.Server
	notReady bool
}

func (s *validatingServer) Predict(ctx context.Context, req *servingv1.PredictRequest) (*servingv1.PredictResponse, error) {
	var in examplev1.DefectInput
	if err := proto.Unmarshal(req.Payload, &in); err != nil {
		return &servingv1.PredictResponse{
			RequestId: req.RequestId,
			ModelName: req.ModelName,
			Status:    servingv1.Status_INVALID_INPUT,
		}, nil
	}
	return s.inner.Predict(ctx, req)
}

func (s *validatingServer) Health(context.Context, *servingv1.HealthRequest) (*servingv1.HealthResponse, error) {
	if s.notReady {
		return &servingv1.HealthResponse{Ready: false, Detail: "model loading"}, nil
	}
	return &servingv1.HealthResponse{Ready: true}, nil
}

// startServer serves srv on an ephemeral loopback port and returns its
// address. envelope.Dial takes a plain host:port target, so a real listener is
// the harness (same shape as internal/traffic's tests).
func startServer(t *testing.T, srv servingv1.ModelServiceServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	servingv1.RegisterModelServiceServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

// newValidatingServer returns a validatingServer whose stub answers every
// Predict with the contents of expected-01.bin — so C5 (exact) and C6 pass.
func newValidatingServer(t *testing.T) *validatingServer {
	t.Helper()
	inner, err := stub.NewServer(filepath.Join(exampleGoldenDir, "expected-01.bin"))
	if err != nil {
		t.Fatalf("stub.NewServer: %v", err)
	}
	return &validatingServer{inner: inner}
}

func runProbe(t *testing.T, target string, m *manifest.Manifest, startupSeconds int) *Report {
	t.Helper()
	return Probe(context.Background(), ProbeConfig{
		Target:         target,
		Manifest:       m,
		GoldenDir:      exampleGoldenDir,
		DescDir:        exampleDescDir,
		StartupSeconds: startupSeconds,
	})
}

func checkByID(t *testing.T, r *Report, id string) CheckResult {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %s not in report: %+v", id, r.Checks)
	return CheckResult{}
}

func TestProbeAllPass(t *testing.T) {
	target := startServer(t, newValidatingServer(t))
	r := runProbe(t, target, exampleManifest(), 10)
	for _, id := range []string{"C2", "C3", "C4", "C5", "C6"} {
		c := checkByID(t, r, id)
		if !c.Pass {
			t.Errorf("%s failed: %s", id, c.Detail)
		}
	}
	if !r.Pass {
		t.Error("report.Pass = false, want true")
	}
	if missing := r.MissingChecks(); len(missing) != 2 { // C1, C7 are the host's
		t.Errorf("MissingChecks = %v, want [C1 C7]", missing)
	}
}

func TestProbeNotReadyFailsC2WithinBudget(t *testing.T) {
	srv := newValidatingServer(t)
	srv.notReady = true
	target := startServer(t, srv)
	start := time.Now()
	r := runProbe(t, target, exampleManifest(), 1)
	elapsed := time.Since(start)
	c2 := checkByID(t, r, "C2")
	if c2.Pass {
		t.Fatal("C2 passed against a never-ready model")
	}
	if !strings.Contains(c2.Detail, "not ready within") || !strings.Contains(c2.Detail, "model loading") {
		t.Errorf("C2 detail %q should name the budget and the last health detail", c2.Detail)
	}
	if elapsed > 5*time.Second {
		t.Errorf("probe took %v against a 1s startup budget; must fail promptly, not hang", elapsed)
	}
	if len(r.Checks) != 1 {
		t.Errorf("checks after C2 failure = %+v, want only C2 (rest not run)", r.Checks)
	}
	if r.Pass {
		t.Error("report.Pass = true after C2 failure")
	}
}

func TestProbeUnreachableTargetFailsC2(t *testing.T) {
	// A model that never opens its port: dial itself must give up by the
	// startup deadline.
	start := time.Now()
	r := runProbe(t, "127.0.0.1:1", exampleManifest(), 1)
	c2 := checkByID(t, r, "C2")
	if c2.Pass {
		t.Fatal("C2 passed against a closed port")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("probe took %v, want prompt failure", elapsed)
	}
}

func TestProbeWrongVersionFailsC3(t *testing.T) {
	srv := newValidatingServer(t)
	srv.inner.Version = "v-evil"
	target := startServer(t, srv)
	c3 := checkByID(t, runProbe(t, target, exampleManifest(), 10), "C3")
	if c3.Pass {
		t.Fatal("C3 passed with a wrong model_version")
	}
	if !strings.Contains(c3.Detail, "v-evil") || !strings.Contains(c3.Detail, stub.DefaultVersion) {
		t.Errorf("C3 detail %q should name both versions", c3.Detail)
	}
}

func TestProbeGarbageAcceptedFailsC3(t *testing.T) {
	// The bare stub does no input validation, so the garbage payload comes
	// back OK — exactly the misbehavior C3(b) must catch.
	inner, err := stub.NewServer(filepath.Join(exampleGoldenDir, "expected-01.bin"))
	if err != nil {
		t.Fatalf("stub.NewServer: %v", err)
	}
	target := startServer(t, inner)
	c3 := checkByID(t, runProbe(t, target, exampleManifest(), 10), "C3")
	if c3.Pass {
		t.Fatal("C3 passed against a model that accepts garbage")
	}
	if !strings.Contains(c3.Detail, "INVALID_INPUT") {
		t.Errorf("C3 detail %q should say INVALID_INPUT was expected", c3.Detail)
	}
}

func TestProbeNonDeterministicFailsC6(t *testing.T) {
	// Non-deterministic model: valid DefectOutput, fresh score each call
	// (Task 9's C6 fixture returns random.random()). Sample tolerance
	// numeric:1000 lets C5 pass; comparisonPolicy exact makes C6 fail —
	// proving C6 compares under the manifest policy, not the pair tolerance.
	expected, err := os.ReadFile(filepath.Join(exampleGoldenDir, "expected-01.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var out examplev1.DefectOutput
	if err := proto.Unmarshal(expected, &out); err != nil {
		t.Fatalf("expected-01.bin does not parse as DefectOutput: %v", err)
	}
	srv := newValidatingServer(t)
	var mu sync.Mutex
	n := 0
	srv.inner.PayloadFunc = func([]byte) []byte {
		mu.Lock()
		n++
		v := n
		mu.Unlock()
		b, err := proto.Marshal(&examplev1.DefectOutput{Label: out.Label, Score: out.Score + float32(v)})
		if err != nil {
			t.Errorf("marshal: %v", err)
		}
		return b
	}
	target := startServer(t, srv)
	m := exampleManifest()
	m.GoldenSamples[0].Tolerance = "numeric:1000"
	r := runProbe(t, target, m, 10)
	if c5 := checkByID(t, r, "C5"); !c5.Pass {
		t.Errorf("C5 should pass under numeric:1000: %s", c5.Detail)
	}
	c6 := checkByID(t, r, "C6")
	if c6.Pass {
		t.Fatal("C6 passed against a non-deterministic model")
	}
	// Policy is exact, so the comparison is byte-level: the detail names the
	// differing calls and the first differing byte.
	if !strings.Contains(c6.Detail, "differ under policy \"exact\"") {
		t.Errorf("C6 detail %q should name the differing calls and the policy", c6.Detail)
	}
}

func TestProbeNonStrictResponseFailsC4(t *testing.T) {
	// Response carries an unknown field (tag 15 varint) on top of valid
	// bytes: parses cleanly, so only the strict (no-unknown-fields) check
	// can catch it.
	srv := newValidatingServer(t)
	srv.inner.PayloadFunc = func(base []byte) []byte {
		return append(append([]byte{}, base...), 0x78, 0x01)
	}
	target := startServer(t, srv)
	c4 := checkByID(t, runProbe(t, target, exampleManifest(), 10), "C4")
	if c4.Pass {
		t.Fatal("C4 passed a response with unknown fields")
	}
	if !strings.Contains(c4.Detail, "unknown") {
		t.Errorf("C4 detail %q should mention unknown fields", c4.Detail)
	}
}

func TestProbeMismatchedGoldenFailsC5(t *testing.T) {
	// Model answers with expected-02 while the manifest expects expected-01.
	inner, err := stub.NewServer(filepath.Join(exampleGoldenDir, "expected-02.bin"))
	if err != nil {
		t.Fatalf("stub.NewServer: %v", err)
	}
	target := startServer(t, &validatingServer{inner: inner})
	c5 := checkByID(t, runProbe(t, target, exampleManifest(), 10), "C5")
	if c5.Pass {
		t.Fatal("C5 passed with the wrong golden output")
	}
	if !strings.Contains(c5.Detail, "sample-01.bin") {
		t.Errorf("C5 detail %q should name the failing sample", c5.Detail)
	}
}

// --- C4 static half ---

func TestCheckSchemaStaticPass(t *testing.T) {
	m := exampleManifest()
	m.GoldenSamples = append(m.GoldenSamples, manifest.GoldenSample{
		Input: "/opt/msp/golden/sample-02.bin", Output: "/opt/msp/golden/expected-02.bin"})
	c := CheckSchemaStatic(m, exampleDescDir, exampleGoldenDir)
	if !c.Pass {
		t.Fatalf("static C4 failed on the real example fixtures: %s", c.Detail)
	}
}

func TestCheckSchemaStaticRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifest.Manifest)
		want   string
	}{
		{"jsonschema input", func(m *manifest.Manifest) { m.Contract.InputSchema.Type = "jsonschema" },
			"not implemented in reference build"},
		{"jsonschema output", func(m *manifest.Manifest) { m.Contract.OutputSchema.Type = "jsonschema" },
			"not implemented in reference build"},
		{"top-k policy", func(m *manifest.Manifest) { m.ComparisonPolicy = "top-k:3" },
			"not implemented in reference build"},
		{"top-k sample tolerance", func(m *manifest.Manifest) { m.GoldenSamples[0].Tolerance = "top-k:1" },
			"not implemented in reference build"},
		{"missing message type", func(m *manifest.Manifest) { m.Contract.OutputSchema.MessageType = "msp.example.v1.Nope" },
			"msp.example.v1.Nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := exampleManifest()
			tt.mutate(m)
			c := CheckSchemaStatic(m, exampleDescDir, exampleGoldenDir)
			if c.Pass {
				t.Fatal("static C4 passed, want fail")
			}
			if !strings.Contains(c.Detail, tt.want) {
				t.Errorf("detail %q does not contain %q", c.Detail, tt.want)
			}
		})
	}
}

func TestCheckSchemaStaticCorruptGolden(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"sample-01.bin", "expected-01.bin"} {
		src, err := os.ReadFile(filepath.Join(exampleGoldenDir, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Corrupt the input sample with bytes that cannot parse.
	if err := os.WriteFile(filepath.Join(dir, "sample-01.bin"), garbagePayload, 0o644); err != nil {
		t.Fatal(err)
	}
	c := CheckSchemaStatic(exampleManifest(), exampleDescDir, dir)
	if c.Pass {
		t.Fatal("static C4 passed with a corrupt golden input")
	}
	if !strings.Contains(c.Detail, "sample-01.bin") {
		t.Errorf("detail %q should name the corrupt file", c.Detail)
	}
}

func TestStrictParseCatchesNonMinimalEncoding(t *testing.T) {
	// An explicitly-encoded zero float (field 2 of DefectOutput) parses to
	// the same message as its absence, so only re-serialization catches it.
	md, err := loadMessageDescriptor(exampleDescDir, exampleManifest().Contract.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte{0x15, 0x00, 0x00, 0x00, 0x00} // score = 0.0, explicitly on the wire
	if err := strictParse(payload, md); err == nil {
		t.Error("strictParse accepted a non-canonical encoding")
	}
	expected, err := os.ReadFile(filepath.Join(exampleGoldenDir, "expected-01.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := strictParse(expected, md); err != nil {
		t.Errorf("strictParse rejected the real golden output: %v", err)
	}
}

func TestGarbagePayloadGenuinelyDoesNotParse(t *testing.T) {
	// Proof for C3(b): the probe's garbage bytes must fail proto parsing
	// (0xDE opens field 27 with invalid wire type 6), not sneak through as
	// an empty or wire-compatible message.
	var in examplev1.DefectInput
	if err := proto.Unmarshal(garbagePayload, &in); err == nil {
		t.Fatalf("garbage payload %x parsed cleanly; C3(b) would be a rubber stamp", garbagePayload)
	}
	if bytes.Equal(garbagePayload, nil) {
		t.Fatal("garbage payload must be non-empty")
	}
}
