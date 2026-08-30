package manifest

import (
	"os"
	"strings"
	"testing"
)

// canonicalSchema is the contract artifact; manifest.schema.json in this package
// is a //go:embed-ed copy of it.
const canonicalSchema = "../../../contract/manifest.schema.json"

// TestSchemaCopyMatchesCanonical makes schema drift a test failure, so `go test
// ./...` catches it without any CI wiring. Run `make sync-schema` to fix.
func TestSchemaCopyMatchesCanonical(t *testing.T) {
	want, err := os.ReadFile(canonicalSchema)
	if err != nil {
		t.Fatalf("read canonical schema: %v", err)
	}
	if string(want) != schemaJSON {
		t.Errorf("embedded manifest.schema.json differs from %s; run `make sync-schema`", canonicalSchema)
	}
}

// specSample is the model-manifest.yaml sample from MSP-SPEC-001 §4.2, quoted
// verbatim -- including its original (non-English) explanatory comments, which
// are part of the quoted spec text. Every other test case is derived from it by
// a single targeted edit, so a failure points at exactly one constraint.
const specSample = `apiVersion: msp/v1
kind: ModelManifest
model:
  name: defect-cls
  version: v4
  description: "Defect classification, retrained 2026-08"
contract:
  protocol: grpc
  port: 8080
  inputSchema:            # protobuf descriptor 或 JSON Schema 擇一
    type: protobuf
    descriptor: /opt/msp/schemas/input.desc
    messageType: fab.defect.v2.WaferImage
  outputSchema:
    type: protobuf
    descriptor: /opt/msp/schemas/output.desc
    messageType: fab.defect.v2.DefectResult
runtime:
  resources:
    requests: {cpu: "2", memory: 4Gi, nvidia.com/gpu: 1}
    limits:   {cpu: "4", memory: 8Gi, nvidia.com/gpu: 1}
  startupSeconds: 120     # conformance 與 readiness 的等待上限
comparisonPolicy: exact   # exact | numeric:<epsilon> | top-k:<k>
                          # C6 與線上 paired diff 共用此 policy;
                          # 非確定性模型(GPU 浮點、sampling)必須宣告非 exact
goldenSamples:            # conformance 用的樣本,image 內附
  - input: /opt/msp/golden/sample-01.bin
    output: /opt/msp/golden/expected-01.bin
    tolerance: exact      # exact | numeric:<epsilon>,未指定則繼承 comparisonPolicy
`

// mutate returns the spec sample with one substring replaced.
func mutate(t *testing.T, old, new string) []byte {
	t.Helper()
	if !strings.Contains(specSample, old) {
		t.Fatalf("spec sample does not contain %q; the test fixture drifted from the spec", old)
	}
	return []byte(strings.Replace(specSample, old, new, 1))
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		raw     []byte
		wantErr bool
		// wantIn are substrings the error must contain: the offending
		// instance path and the reason.
		wantIn []string
	}{
		{
			name: "spec 4.2 sample is valid",
			raw:  []byte(specSample),
		},
		{
			name:    "missing model.name",
			raw:     mutate(t, "  name: defect-cls\n", ""),
			wantErr: true,
			wantIn:  []string{"/model", "name"},
		},
		{
			name:    "unknown comparisonPolicy",
			raw:     mutate(t, "comparisonPolicy: exact", "comparisonPolicy: fuzzy"),
			wantErr: true,
			wantIn:  []string{"/comparisonPolicy"},
		},
		{
			name:    "port other than 8080",
			raw:     mutate(t, "  port: 8080", "  port: 9090"),
			wantErr: true,
			wantIn:  []string{"/contract/port"},
		},
		{
			name: "empty goldenSamples",
			raw: []byte(strings.SplitN(specSample, "goldenSamples:", 2)[0] +
				"goldenSamples: []\n"),
			wantErr: true,
			wantIn:  []string{"/goldenSamples"},
		},
		{
			name:    "protobuf schemaRef without messageType",
			raw:     mutate(t, "    messageType: fab.defect.v2.WaferImage\n", ""),
			wantErr: true,
			wantIn:  []string{"/contract/inputSchema", "messageType"},
		},
		{
			name:    "startupSeconds above 600",
			raw:     mutate(t, "  startupSeconds: 120", "  startupSeconds: 601"),
			wantErr: true,
			wantIn:  []string{"/runtime/startupSeconds"},
		},
		{
			name:    "not YAML",
			raw:     []byte("\tnot: yaml"),
			wantErr: true,
		},
		// The contract permits these two; rejecting them is the conformance
		// tool's job (with an explicit "not implemented" error), not the
		// schema's.
		{
			name: "top-k comparisonPolicy is accepted",
			raw:  mutate(t, "comparisonPolicy: exact", "comparisonPolicy: top-k:5"),
		},
		{
			name: "jsonschema inputSchema is accepted",
			raw: mutate(t,
				"    type: protobuf\n    descriptor: /opt/msp/schemas/input.desc\n    messageType: fab.defect.v2.WaferImage\n",
				"    type: jsonschema\n    descriptor: /opt/msp/schemas/input.json\n"),
		},
		{
			name: "numeric comparisonPolicy is accepted",
			raw:  mutate(t, "comparisonPolicy: exact", "comparisonPolicy: numeric:1e-6"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() = %+v, want error", got)
				}
				for _, want := range tt.wantIn {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not name %q: %v", want, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got == nil {
				t.Fatal("Load() returned nil manifest without error")
			}
		})
	}
}

func TestLoadSpecSampleFields(t *testing.T) {
	m, err := Load([]byte(specSample))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if m.APIVersion != "msp/v1" || m.Kind != "ModelManifest" {
		t.Errorf("apiVersion/kind = %q/%q", m.APIVersion, m.Kind)
	}
	if m.Model.Name != "defect-cls" || m.Model.Version != "v4" {
		t.Errorf("model = %+v", m.Model)
	}
	if m.Contract.Protocol != "grpc" || m.Contract.Port != 8080 {
		t.Errorf("contract = %+v", m.Contract)
	}
	if m.Contract.InputSchema.MessageType != "fab.defect.v2.WaferImage" {
		t.Errorf("inputSchema = %+v", m.Contract.InputSchema)
	}
	if m.Contract.OutputSchema.Descriptor != "/opt/msp/schemas/output.desc" {
		t.Errorf("outputSchema = %+v", m.Contract.OutputSchema)
	}
	if m.Runtime.StartupSeconds != 120 {
		t.Errorf("startupSeconds = %d", m.Runtime.StartupSeconds)
	}
	// Quantities are strings even when the YAML scalar looks numeric.
	if got := m.Runtime.Resources.Requests["nvidia.com/gpu"]; got != "1" {
		t.Errorf(`requests["nvidia.com/gpu"] = %q, want "1"`, got)
	}
	if got := m.Runtime.Resources.Limits["memory"]; got != "8Gi" {
		t.Errorf(`limits["memory"] = %q, want "8Gi"`, got)
	}
	if m.ComparisonPolicy != "exact" {
		t.Errorf("comparisonPolicy = %q", m.ComparisonPolicy)
	}
	if len(m.GoldenSamples) != 1 {
		t.Fatalf("goldenSamples = %+v", m.GoldenSamples)
	}
	want := GoldenSample{
		Input:     "/opt/msp/golden/sample-01.bin",
		Output:    "/opt/msp/golden/expected-01.bin",
		Tolerance: "exact",
	}
	if m.GoldenSamples[0] != want {
		t.Errorf("goldenSamples[0] = %+v, want %+v", m.GoldenSamples[0], want)
	}
}
