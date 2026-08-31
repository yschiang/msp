package envelope

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	examplev1 "github.com/yschiang/msp/msp/gen/examplev1"
	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"github.com/yschiang/msp/msp/internal/manifest"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Paths into the committed example model, relative to this package directory.
const (
	exampleDir    = "../../../contract/examples/defect-cls"
	exampleGolden = exampleDir + "/golden"
	exampleSchema = exampleDir + "/schemas"
)

// defectOutputRef mimics what a real manifest declares: an absolute container
// path. Compare must resolve it against descDir by basename.
var defectOutputRef = manifest.SchemaRef{
	Type:        "protobuf",
	Descriptor:  "/opt/msp/schemas/output.desc",
	MessageType: "msp.example.v1.DefectOutput",
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func defectOutput(label string, score float32) *examplev1.DefectOutput {
	return &examplev1.DefectOutput{Label: label, Score: score}
}

// --- LoadGoldens ---

func TestLoadGoldensFromExampleManifest(t *testing.T) {
	raw, err := os.ReadFile(exampleDir + "/model-manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(raw)
	if err != nil {
		t.Fatal(err)
	}

	pairs, err := LoadGoldens(m, exampleGolden)
	if err != nil {
		t.Fatalf("LoadGoldens: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs, want 2", len(pairs))
	}
	for i, name := range []string{"sample-01.bin", "expected-01.bin"} {
		want, err := os.ReadFile(filepath.Join(exampleGolden, name))
		if err != nil {
			t.Fatal(err)
		}
		got := [][]byte{pairs[0].Input, pairs[0].Expected}[i]
		if !bytes.Equal(got, want) {
			t.Errorf("pair 0 %s: bytes differ from file on disk", name)
		}
	}
	// Manifest samples declare no per-sample tolerance, so both inherit
	// comparisonPolicy: exact.
	for i, p := range pairs {
		if p.Tolerance != "exact" {
			t.Errorf("pair %d tolerance = %q, want inherited %q", i, p.Tolerance, "exact")
		}
	}
}

func TestLoadGoldensToleranceOverride(t *testing.T) {
	m := &manifest.Manifest{ComparisonPolicy: "exact"}
	m.GoldenSamples = []manifest.GoldenSample{{
		Input:     "/opt/msp/golden/sample-01.bin",
		Output:    "/opt/msp/golden/expected-01.bin",
		Tolerance: "numeric:0.5",
	}}
	pairs, err := LoadGoldens(m, exampleGolden)
	if err != nil {
		t.Fatal(err)
	}
	if pairs[0].Tolerance != "numeric:0.5" {
		t.Errorf("tolerance = %q, want per-sample override %q", pairs[0].Tolerance, "numeric:0.5")
	}
}

func TestLoadGoldensMissingFile(t *testing.T) {
	m := &manifest.Manifest{ComparisonPolicy: "exact"}
	m.GoldenSamples = []manifest.GoldenSample{{
		Input:  "/opt/msp/golden/no-such.bin",
		Output: "/opt/msp/golden/expected-01.bin",
	}}
	_, err := LoadGoldens(m, exampleGolden)
	if err == nil {
		t.Fatal("want error for missing golden file, got nil")
	}
	if !strings.Contains(err.Error(), "no-such.bin") {
		t.Errorf("error %q does not name the missing file", err)
	}
}

// --- Compare: tolerance strings and DefectOutput cases (brief Step 1) ---

func TestCompareDefectOutput(t *testing.T) {
	// score values chosen so a 1e-9 delta survives float32 rounding: the ulp
	// at 0.001 is ~1.2e-10, so these two encode to different bytes.
	base := mustMarshal(t, defectOutput("defect", 0.001))
	nudged := mustMarshal(t, defectOutput("defect", float32(0.001+1e-9)))
	if bytes.Equal(base, nudged) {
		t.Fatal("test setup: 1e-9 nudge vanished in float32 rounding; pick a smaller base score")
	}

	tests := []struct {
		name              string
		expected, actual  []byte
		tolerance         string
		wantErr           bool
		wantErrSubstrings []string
	}{
		{"identical exact", base, base, "exact", false, nil},
		{"1e-9 delta fails exact", base, nudged, "exact", true, nil},
		{"1e-9 delta passes numeric", base, nudged, "numeric:0.001", false, nil},
		{
			"label differs fails exact",
			mustMarshal(t, defectOutput("defect", 0.75)),
			mustMarshal(t, defectOutput("ok", 0.75)),
			"exact", true, nil,
		},
		{
			"label differs fails numeric, error names field and values",
			mustMarshal(t, defectOutput("defect", 0.75)),
			mustMarshal(t, defectOutput("ok", 0.75)),
			"numeric:0.001", true, []string{"label", "defect", "ok"},
		},
		{
			"score beyond eps fails numeric, error names field and values",
			mustMarshal(t, defectOutput("defect", 0.75)),
			mustMarshal(t, defectOutput("defect", 0.25)),
			"numeric:0.001", true, []string{"score", "0.75", "0.25"},
		},
		{"top-k errors", base, base, "top-k:3", true, []string{"not implemented in reference build"}},
		{"unknown tolerance errors", base, base, "fuzzy", true, []string{"fuzzy"}},
		{"bad epsilon errors", base, nudged, "numeric:abc", true, []string{"abc"}},
		{"negative epsilon errors", base, nudged, "numeric:-1", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Compare(tt.expected, tt.actual, tt.tolerance, defectOutputRef, exampleSchema)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Compare() error = %v, wantErr %v", err, tt.wantErr)
			}
			for _, s := range tt.wantErrSubstrings {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
		})
	}
}

func TestCompareExactIgnoresDescriptor(t *testing.T) {
	// "exact" is byte equality; it must not fail because the descriptor
	// directory is unreadable (lazy descriptor loading, by design).
	b := mustMarshal(t, defectOutput("defect", 0.75))
	if err := Compare(b, b, "exact", defectOutputRef, "/nonexistent"); err != nil {
		t.Errorf("exact compare touched the descriptor: %v", err)
	}
}

func TestCompareNumericMissingDescriptor(t *testing.T) {
	b := mustMarshal(t, defectOutput("defect", 0.75))
	err := Compare(b, b, "numeric:0.001", defectOutputRef, t.TempDir())
	if err == nil {
		t.Fatal("want error for missing descriptor file, got nil")
	}
	if !strings.Contains(err.Error(), "output.desc") {
		t.Errorf("error %q does not name the descriptor file", err)
	}
}

func TestCompareNumericRejectsNonProtobufSchema(t *testing.T) {
	b := mustMarshal(t, defectOutput("defect", 0.75))
	ref := manifest.SchemaRef{Type: "jsonschema", Descriptor: "/opt/msp/schemas/out.json"}
	if err := Compare(b, b, "numeric:0.001", ref, t.TempDir()); err == nil {
		t.Fatal("want error for non-protobuf output schema under numeric tolerance")
	}
}

// --- Compare: reflection walk over nested messages, maps, repeated fields ---
//
// DefectOutput is a flat two-field message, far too simple to prove the walker
// doesn't silently skip structure. These tests build a descriptor set from the
// envelope's own types (PredictRequest: nested Timestamp message + string map;
// DefectInput: repeated float) and hunt false negatives: every "wantErr" case
// here is a difference a lazy walker would swallow.

func writeRichDesc(t *testing.T) string {
	t.Helper()
	fdset := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto((&timestamppb.Timestamp{}).ProtoReflect().Descriptor().ParentFile()),
		protodesc.ToFileDescriptorProto((&servingv1.PredictRequest{}).ProtoReflect().Descriptor().ParentFile()),
		protodesc.ToFileDescriptorProto((&examplev1.DefectInput{}).ProtoReflect().Descriptor().ParentFile()),
	}}
	raw, err := proto.Marshal(fdset)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rich.desc"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func predictReq(seconds int64, meta map[string]string) *servingv1.PredictRequest {
	var ts *timestamppb.Timestamp
	if seconds != 0 {
		ts = &timestamppb.Timestamp{Seconds: seconds}
	}
	return &servingv1.PredictRequest{RequestId: "r", IngestTime: ts, Metadata: meta}
}

func features(f ...float32) *examplev1.DefectInput {
	return &examplev1.DefectInput{WaferId: "w", Features: f}
}

func TestCompareWalksStructure(t *testing.T) {
	descDir := writeRichDesc(t)
	reqRef := manifest.SchemaRef{
		Type:        "protobuf",
		Descriptor:  "/opt/msp/schemas/rich.desc",
		MessageType: "msp.serving.v1.PredictRequest",
	}
	inRef := manifest.SchemaRef{
		Type:        "protobuf",
		Descriptor:  "/opt/msp/schemas/rich.desc",
		MessageType: "msp.example.v1.DefectInput",
	}

	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	ninf := float32(math.Inf(-1))

	tests := []struct {
		name              string
		expected, actual  proto.Message
		ref               manifest.SchemaRef
		wantErr           bool
		wantErrSubstrings []string
	}{
		{
			"equal nested and map",
			predictReq(5, map[string]string{"a": "1"}),
			predictReq(5, map[string]string{"a": "1"}),
			reqRef, false, nil,
		},
		{
			"nested field differs",
			predictReq(5, nil), predictReq(6, nil),
			reqRef, true, []string{"ingest_time.seconds", "5", "6"},
		},
		{
			"nested message set vs unset",
			predictReq(5, nil), predictReq(0, nil),
			reqRef, true, []string{"ingest_time"},
		},
		{
			"map key missing in actual",
			predictReq(5, map[string]string{"a": "1"}), predictReq(5, nil),
			reqRef, true, []string{`metadata["a"]`},
		},
		{
			"map key extra in actual",
			predictReq(5, nil), predictReq(5, map[string]string{"b": "1"}),
			reqRef, true, []string{`metadata["b"]`},
		},
		{
			"map value differs",
			predictReq(5, map[string]string{"a": "1"}),
			predictReq(5, map[string]string{"a": "2"}),
			reqRef, true, []string{`metadata["a"]`, "1", "2"},
		},
		{
			"repeated length differs",
			features(1, 2), features(1),
			inRef, true, []string{"features", "2", "1"},
		},
		{
			"repeated element beyond eps",
			features(1, 2), features(1, 2.5),
			inRef, true, []string{"features[1]", "2", "2.5"},
		},
		{
			"repeated element within eps",
			features(1, 2), features(1, 2.00001),
			inRef, false, nil,
		},
		{"NaN equals NaN", features(nan), features(nan), inRef, false, nil},
		{"NaN vs number", features(nan), features(1), inRef, true, []string{"features[0]", "NaN"}},
		{"+Inf equals +Inf", features(inf), features(inf), inRef, false, nil},
		{"+Inf vs -Inf", features(inf), features(ninf), inRef, true, []string{"features[0]", "Inf"}},
		{"+Inf vs finite", features(inf), features(1e30), inRef, true, []string{"features[0]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Compare(mustMarshal(t, tt.expected), mustMarshal(t, tt.actual),
				"numeric:0.001", tt.ref, descDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Compare() error = %v, wantErr %v", err, tt.wantErr)
			}
			for _, s := range tt.wantErrSubstrings {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
		})
	}
}

func TestCompareUnknownFieldsDiffer(t *testing.T) {
	descDir := writeRichDesc(t)
	inRef := manifest.SchemaRef{
		Type:        "protobuf",
		Descriptor:  "/opt/msp/schemas/rich.desc",
		MessageType: "msp.example.v1.DefectInput",
	}
	expected := mustMarshal(t, features(1))
	// Same message plus one field the descriptor doesn't know: field 99,
	// varint 1 (tag 99<<3|0 = 792 -> 0x98 0x06). A walker that only visits
	// declared fields would silently pass this.
	actual := append(append([]byte{}, expected...), 0x98, 0x06, 0x01)
	err := Compare(expected, actual, "numeric:0.001", inRef, descDir)
	if err == nil {
		t.Fatal("want error for unknown-field difference, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error %q does not mention unknown fields", err)
	}
}
