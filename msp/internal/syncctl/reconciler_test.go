package syncctl

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mspv1 "github.com/yschiang/msp/msp/api/v1"
	"github.com/yschiang/msp/msp/internal/conformance"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := mspv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func passReport() *conformance.Report {
	r := &conformance.Report{}
	for _, id := range conformance.AllCheckIDs {
		r.Checks = append(r.Checks, conformance.CheckResult{ID: id, Name: conformance.CheckNames[id], Pass: true})
	}
	r.Aggregate()
	return r
}

func failC3Report() *conformance.Report {
	r := passReport()
	r.Checks[2] = conformance.CheckResult{ID: "C3", Name: "envelope", Pass: false, Detail: "OK returned for garbage"}
	r.Aggregate()
	return r
}

// newReconciler wires a fake client and happy-path fakes for every external
// call; tests override the ones they exercise.
func newReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&mspv1.ModelDeployment{}, &appsv1.Deployment{}).Build()
	r := &Reconciler{
		Client:            c,
		ClusterNamespaces: DefaultClusterNamespaces,
		Registries:        Registries{ModelCenter: "localhost:5010", Internal: "localhost:5011"},
		ResolveDigest:     func(context.Context, string) (string, error) { return goodDigest, nil },
		ExtractManifest:   func(context.Context, string) ([]byte, error) { return []byte(exampleManifest), nil },
		Verify:            func(context.Context, string) (*conformance.Report, error) { return passReport(), nil },
		CopyImage:         func(_ context.Context, _, _ string) (string, error) { return goodDigest, nil },
	}
	return r, c
}

func key(md *mspv1.ModelDeployment) types.NamespacedName {
	return types.NamespacedName{Namespace: md.Namespace, Name: md.Name}
}

func get(t *testing.T, c client.Client, md *mspv1.ModelDeployment) *mspv1.ModelDeployment {
	t.Helper()
	out := &mspv1.ModelDeployment{}
	if err := c.Get(context.Background(), key(md), out); err != nil {
		t.Fatal(err)
	}
	return out
}

// driveTo reconciles until status.phase == want, waiting out background
// runs. Landing in Rejected while aiming elsewhere is a test failure with
// the reason attached.
func driveTo(t *testing.T, r *Reconciler, c client.Client, md *mspv1.ModelDeployment, want string) *mspv1.ModelDeployment {
	t.Helper()
	for i := 0; i < 50; i++ {
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(md)})
		if err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
		if res.RequeueAfter > 0 {
			time.Sleep(10 * time.Millisecond)
		}
		cur := get(t, c, md)
		if cur.Status.Phase == want {
			return cur
		}
		if cur.Status.Phase == mspv1.PhaseRejected {
			t.Fatalf("Rejected while driving to %s: %s", want, cur.Status.Message)
		}
	}
	t.Fatalf("never reached %s; last phase %q", want, get(t, c, md).Status.Phase)
	return nil
}

func TestClusterNamespaceMismatchIsRejected(t *testing.T) {
	md := newMD()
	md.Namespace = "msp-blue" // says green, lives in blue
	r, c := newReconciler(t, md)
	got := driveTo(t, r, c, md, mspv1.PhaseRejected)
	if got.Status.Message == "" || got.Spec.ModelRef.Digest != "" {
		t.Errorf("want a reason and no digest work: %+v", got)
	}
}

func TestHappyPathPinsRunsCopiesDeploys(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)

	got := driveTo(t, r, c, md, mspv1.PhaseDeployable)
	if got.Spec.ModelRef.Digest != goodDigest || got.Status.PinnedDigest != goodDigest {
		t.Errorf("digest not pinned in both places: spec=%q status=%q", got.Spec.ModelRef.Digest, got.Status.PinnedDigest)
	}
	if len(got.Status.Conformance.Passed) != 7 || len(got.Status.Conformance.Failed) != 0 {
		t.Errorf("conformance status %+v", got.Status.Conformance)
	}

	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), key(md), dep); err != nil {
		t.Fatal(err)
	}
	wantImage := "localhost:5011/defect-cls@" + goodDigest
	if img := dep.Spec.Template.Spec.Containers[0].Image; img != wantImage {
		t.Errorf("image %q, want %q", img, wantImage)
	}
	if len(dep.OwnerReferences) != 1 || dep.OwnerReferences[0].Kind != "ModelDeployment" || !*dep.OwnerReferences[0].Controller {
		t.Errorf("ownerReferences %+v (D19)", dep.OwnerReferences)
	}
	for _, obj := range []client.Object{&corev1.Service{}, &autoscalingv2.HorizontalPodAutoscaler{}} {
		if err := c.Get(context.Background(), key(md), obj); err != nil {
			t.Errorf("%T: %v", obj, err)
		} else if len(obj.GetOwnerReferences()) != 1 {
			t.Errorf("%T has no owner", obj)
		}
	}

	// D4: Deployed follows ready replicas, both ways.
	dep.Status.ReadyReplicas = 1
	if err := c.Status().Update(context.Background(), dep); err != nil {
		t.Fatal(err)
	}
	driveTo(t, r, c, md, mspv1.PhaseDeployed)
	dep.Status.ReadyReplicas = 0
	if err := c.Status().Update(context.Background(), dep); err != nil {
		t.Fatal(err)
	}
	driveTo(t, r, c, md, mspv1.PhaseDeployable)
}

// A restart leaves a Deployed object with a cold manifest cache. The re-read
// must come from our own copy, not from model-center, which may have deleted
// or retagged the image by then (D8).
func TestDeployReExtractsFromTheInternalRegistry(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)
	driveTo(t, r, c, md, mspv1.PhaseDeployable)

	fresh, _ := newReconciler(t) // a restarted controller: same cluster, empty cache
	fresh.Client = c
	var refs []string
	fresh.ExtractManifest = func(_ context.Context, ref string) ([]byte, error) {
		refs = append(refs, ref)
		return []byte(exampleManifest), nil
	}
	if _, err := fresh.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(md)}); err != nil {
		t.Fatal(err)
	}
	want := []string{"localhost:5011/defect-cls@" + goodDigest}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("ExtractManifest refs %v, want %v", refs, want)
	}
}

func TestPresetDigestIsNeverReResolved(t *testing.T) {
	md := newMD()
	md.Spec.ModelRef.Digest = badDigest
	md.Status = mspv1.ModelDeploymentStatus{Phase: mspv1.PhaseSyncing}
	r, c := newReconciler(t, md)
	r.ResolveDigest = func(context.Context, string) (string, error) {
		t.Error("ResolveDigest called although spec.modelRef.digest is set")
		return goodDigest, nil
	}
	r.CopyImage = func(_ context.Context, _, _ string) (string, error) { return badDigest, nil }
	got := driveTo(t, r, c, md, mspv1.PhaseDeployable)
	if got.Status.PinnedDigest != badDigest {
		t.Errorf("pinned %q, want the preset %q", got.Status.PinnedDigest, badDigest)
	}
}

func TestCRVersionMismatchIsRejected(t *testing.T) {
	md := newMD()
	md.Spec.ModelRef.Version = "v2" // manifest says v1
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)
	got := driveTo(t, r, c, md, mspv1.PhaseRejected)
	if got.Status.PinnedDigest != goodDigest {
		t.Error("digest should have been pinned before the manifest check")
	}
	if len(got.Status.Conformance.Failed) != 0 {
		t.Errorf("rejected in Syncing must leave conformance.failed empty: %+v", got.Status.Conformance)
	}
}

func TestMissingManifestIsRejectedInSyncing(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)
	r.ExtractManifest = func(context.Context, string) ([]byte, error) {
		return nil, conformance.ErrNoManifest
	}
	verifyCalled := false
	r.Verify = func(context.Context, string) (*conformance.Report, error) {
		verifyCalled = true
		return passReport(), nil
	}
	got := driveTo(t, r, c, md, mspv1.PhaseRejected)
	if verifyCalled || len(got.Status.Conformance.Failed) != 0 {
		t.Errorf("must not reach conformance: called=%v status=%+v", verifyCalled, got.Status.Conformance)
	}
}

func TestSchemaInvalidManifestIsRejectedInSyncing(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)
	r.ExtractManifest = func(context.Context, string) ([]byte, error) {
		return []byte("apiVersion: msp/v1\nkind: ModelManifest\n"), nil
	}
	got := driveTo(t, r, c, md, mspv1.PhaseRejected)
	if got.Status.Message == "" {
		t.Error("want the schema error in status.message")
	}
}

func TestFailingReportIsRejectedWithCheckNamed(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)
	r.Verify = func(context.Context, string) (*conformance.Report, error) { return failC3Report(), nil }
	got := driveTo(t, r, c, md, mspv1.PhaseRejected)
	if len(got.Status.Conformance.Failed) != 1 || got.Status.Conformance.Failed[0] != "C3" {
		t.Errorf("failed %v, want [C3]", got.Status.Conformance.Failed)
	}
	if len(got.Status.Conformance.Passed) != 6 {
		t.Errorf("passed %v", got.Status.Conformance.Passed)
	}
	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), key(md), dep); err == nil {
		t.Error("Rejected must not create a Deployment")
	}
}

func TestInternalDigestMismatchIsRejectedLoudly(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)
	r.CopyImage = func(_ context.Context, _, _ string) (string, error) { return badDigest, nil }
	got := driveTo(t, r, c, md, mspv1.PhaseRejected)
	for _, want := range []string{goodDigest, badDigest} {
		if !strings.Contains(got.Status.Message, want) {
			t.Errorf("message must name both digests, got %q", got.Status.Message)
		}
	}
}

func TestInfrastructureErrorsRequeueWithoutChangingPhase(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)
	r.ResolveDigest = func(context.Context, string) (string, error) { return "", errors.New("registry down") }
	driveTo(t, r, c, md, mspv1.PhaseSyncing)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(md)}); err == nil {
		t.Fatal("want an error so controller-runtime backs off (D6)")
	}
	got := get(t, c, md)
	if got.Status.Phase != mspv1.PhaseSyncing || got.Spec.ModelRef.Digest != "" {
		t.Errorf("phase/digest must be untouched: %+v", got)
	}

	// Same rule in ConformanceRunning: a VerifyImage error is retried, not a verdict.
	r.ResolveDigest = func(context.Context, string) (string, error) { return goodDigest, nil }
	calls := 0
	r.Verify = func(context.Context, string) (*conformance.Report, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("docker down")
		}
		return passReport(), nil
	}
	driveTo(t, r, c, md, mspv1.PhaseConformanceRunning)
	var lastErr error
	for i := 0; i < 20 && lastErr == nil; i++ {
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(md)})
		lastErr = err
		if res.RequeueAfter > 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if lastErr == nil {
		t.Fatal("first Verify error must surface as a reconcile error")
	}
	if get(t, c, md).Status.Phase != mspv1.PhaseConformanceRunning {
		t.Error("phase must stay ConformanceRunning after an infra error")
	}
	driveTo(t, r, c, md, mspv1.PhaseDeployable)
	if calls != 2 {
		t.Errorf("Verify calls = %d, want 2 (one failure, one retry)", calls)
	}
}

func TestOneConformanceRunPerDigest(t *testing.T) {
	a, b := newMD(), newMD()
	b.Name = "defect-cls-v1-again"
	a.Status, b.Status = mspv1.ModelDeploymentStatus{}, mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, a, b)
	var calls int32
	r.Verify = func(context.Context, string) (*conformance.Report, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond)
		return passReport(), nil
	}
	driveTo(t, r, c, a, mspv1.PhaseConformanceRunning)
	driveTo(t, r, c, b, mspv1.PhaseConformanceRunning)
	driveTo(t, r, c, a, mspv1.PhaseDeployable)
	driveTo(t, r, c, b, mspv1.PhaseDeployable)
	if calls != 1 {
		t.Errorf("Verify called %d times for one digest, want 1 (D12)", calls)
	}
}

func TestReplicasWrittenOnceEverythingElseReconciled(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{}
	r, c := newReconciler(t, md)
	driveTo(t, r, c, md, mspv1.PhaseDeployable)

	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), key(md), dep); err != nil {
		t.Fatal(err)
	}
	five := int32(5)
	dep.Spec.Replicas = &five // the HPA scaled it
	dep.Spec.Template.Spec.Containers[0].Image = "evil:latest"
	if err := c.Update(context.Background(), dep); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(md)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key(md), dep); err != nil {
		t.Fatal(err)
	}
	if *dep.Spec.Replicas != 5 {
		t.Errorf("replicas %d: the controller must not fight the HPA (D15)", *dep.Spec.Replicas)
	}
	if img := dep.Spec.Template.Spec.Containers[0].Image; img != "localhost:5011/defect-cls@"+goodDigest {
		t.Errorf("image %q not driven back to the pinned digest (D16)", img)
	}
}

func TestRejectedIsTerminal(t *testing.T) {
	md := newMD()
	md.Status = mspv1.ModelDeploymentStatus{Phase: mspv1.PhaseRejected, Message: "kept"}
	r, c := newReconciler(t, md)
	called := false
	r.ResolveDigest = func(context.Context, string) (string, error) { called = true; return goodDigest, nil }
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(md)}); err != nil {
		t.Fatal(err)
	}
	got := get(t, c, md)
	if called || got.Status.Phase != mspv1.PhaseRejected || got.Status.Message != "kept" {
		t.Errorf("Rejected must be left alone (D5): called=%v %+v", called, got.Status)
	}
}
