package syncctl

import (
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mspv1 "github.com/yschiang/msp/msp/api/v1"
	"github.com/yschiang/msp/msp/internal/manifest"
)

// exampleManifest is contract/examples/defect-cls/model-manifest.yaml minus
// the description, inlined so the package tests need no docker and no
// relative path out of the module.
const exampleManifest = `apiVersion: msp/v1
kind: ModelManifest
model:
  name: defect-cls
  version: v1
contract:
  protocol: grpc
  port: 8080
  inputSchema:
    type: protobuf
    descriptor: /opt/msp/schemas/input.desc
    messageType: msp.example.v1.DefectInput
  outputSchema:
    type: protobuf
    descriptor: /opt/msp/schemas/output.desc
    messageType: msp.example.v1.DefectOutput
runtime:
  resources:
    requests: {cpu: "1", memory: 1Gi}
    limits: {cpu: "2", memory: 2Gi}
  startupSeconds: 30
comparisonPolicy: exact
goldenSamples:
  - input: /opt/msp/golden/sample-01.bin
    output: /opt/msp/golden/expected-01.bin
`

const (
	goodDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	badDigest  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func loadExample(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Load([]byte(exampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func newMD() *mspv1.ModelDeployment {
	return &mspv1.ModelDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "defect-cls-v1", Namespace: "msp-green"},
		Spec: mspv1.ModelDeploymentSpec{
			ModelRef: mspv1.ModelRef{Source: "model-center", Model: "defect-cls", Version: "v1"},
			Cluster:  "green",
			Replicas: mspv1.Replicas{Min: 1, Max: 2},
		},
		Status: mspv1.ModelDeploymentStatus{PinnedDigest: goodDigest},
	}
}

func TestBuildDeployment(t *testing.T) {
	md := newMD()
	image := "localhost:5011/defect-cls@" + goodDigest
	dep, err := BuildDeployment(md, loadExample(t), image)
	if err != nil {
		t.Fatal(err)
	}
	if dep.Name != "defect-cls-v1" || dep.Namespace != "msp-green" {
		t.Errorf("name/namespace: %s/%s", dep.Namespace, dep.Name)
	}
	if *dep.Spec.Replicas != 1 {
		t.Errorf("replicas at creation must be replicas.min (D15), got %d", *dep.Spec.Replicas)
	}
	if dep.Spec.Selector.MatchLabels[LabelDeployment] != "defect-cls-v1" ||
		dep.Spec.Template.Labels[LabelDeployment] != "defect-cls-v1" {
		t.Errorf("selector/template labels: %v / %v", dep.Spec.Selector.MatchLabels, dep.Spec.Template.Labels)
	}
	c := dep.Spec.Template.Spec.Containers
	if len(c) != 1 {
		t.Fatalf("want one container, got %d", len(c))
	}
	if c[0].Image != image {
		t.Errorf("image %q, want the internal digest ref %q", c[0].Image, image)
	}
	if c[0].Ports[0].ContainerPort != 8080 || c[0].Ports[0].Name != "grpc" {
		t.Errorf("port: %+v", c[0].Ports)
	}
	if got := c[0].Resources.Requests[corev1.ResourceCPU]; got.String() != "1" {
		t.Errorf("requests.cpu %s", got.String())
	}
	if got := c[0].Resources.Limits[corev1.ResourceMemory]; got.String() != "2Gi" {
		t.Errorf("limits.memory %s", got.String())
	}
	if c[0].Env[0].Name != "MSP_MODEL_DIGEST" || c[0].Env[0].Value != goodDigest {
		t.Errorf("env: %+v", c[0].Env)
	}
	// D17: gRPC startup probe with failureThreshold = startupSeconds at 1s,
	// gRPC readiness 5s/3, no liveness.
	sp := c[0].StartupProbe
	if sp == nil || sp.GRPC == nil || sp.GRPC.Port != 8080 || sp.PeriodSeconds != 1 || sp.FailureThreshold != 30 {
		t.Errorf("startupProbe: %+v", sp)
	}
	rp := c[0].ReadinessProbe
	if rp == nil || rp.GRPC == nil || rp.GRPC.Port != 8080 || rp.PeriodSeconds != 5 || rp.FailureThreshold != 3 {
		t.Errorf("readinessProbe: %+v", rp)
	}
	if c[0].LivenessProbe != nil {
		t.Error("no livenessProbe (D17)")
	}
}

func TestBuildDeploymentRejectsBadQuantity(t *testing.T) {
	m := loadExample(t)
	m.Runtime.Resources.Limits["cpu"] = "two"
	if _, err := BuildDeployment(newMD(), m, "x"); err == nil {
		t.Fatal("want error for unparsable quantity")
	}
}

func TestBuildService(t *testing.T) {
	svc := BuildService(newMD(), 8080)
	if svc.Spec.Selector[LabelDeployment] != "defect-cls-v1" {
		t.Errorf("selector %v", svc.Spec.Selector)
	}
	p := svc.Spec.Ports[0]
	if p.Name != "grpc" || p.Port != 8080 || p.TargetPort.IntValue() != 8080 {
		t.Errorf("port %+v", p)
	}
}

func TestBuildHPA(t *testing.T) {
	hpa := BuildHPA(newMD())
	if hpa.Spec.ScaleTargetRef.Kind != "Deployment" || hpa.Spec.ScaleTargetRef.Name != "defect-cls-v1" {
		t.Errorf("target %+v", hpa.Spec.ScaleTargetRef)
	}
	if *hpa.Spec.MinReplicas != 1 || hpa.Spec.MaxReplicas != 2 {
		t.Errorf("min/max %d/%d", *hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
	}
	if len(hpa.Spec.Metrics) != 1 {
		t.Fatalf("want exactly one metric, got %d", len(hpa.Spec.Metrics))
	}
	m := hpa.Spec.Metrics[0]
	if m.Type != autoscalingv2.PodsMetricSourceType || m.Pods == nil || m.Pods.Metric.Name != InflightMetric {
		t.Errorf("metric must be the pods metric %s, never CPU (spec §5.2): %+v", InflightMetric, m)
	}
}
