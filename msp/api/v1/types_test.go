package v1

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestSchemeRegistersModelDeployment(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if !s.Recognizes(GroupVersion.WithKind("ModelDeployment")) {
		t.Fatal("ModelDeployment not registered under msp.platform/v1")
	}
}

func TestJSONShapeMatchesSpec52(t *testing.T) {
	md := ModelDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "defect-cls-v1", Namespace: "msp-green"},
		Spec: ModelDeploymentSpec{
			ModelRef: ModelRef{Source: "model-center", Model: "defect-cls", Version: "v1"},
			Cluster:  "green",
			Replicas: Replicas{Min: 2, Max: 8},
		},
		Status: ModelDeploymentStatus{Phase: PhaseDeployed, PinnedDigest: "sha256:abc",
			Conformance: ConformanceStatus{Passed: []string{"C1"}}},
	}
	raw, err := json.Marshal(md)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"modelRef":{"source":"model-center","model":"defect-cls","version":"v1"}`,
		`"cluster":"green"`, `"replicas":{"min":2,"max":8}`,
		`"phase":"Deployed"`, `"pinnedDigest":"sha256:abc"`, `"conformance":{"passed":["C1"]}`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"digest"`) {
		t.Errorf("empty digest must be omitted (CEL immutability rule keys off its absence): %s", raw)
	}
}
