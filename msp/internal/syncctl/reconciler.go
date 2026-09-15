package syncctl

// This file: the Sync Controller's state machine (Phase 1 design §2).
//
//   "" ──map ok──▶ Syncing ──pinned, manifest ok──▶ ConformanceRunning
//                    │                                   │ all-pass, copy verified
//                    ▼                                   ▼
//                 Rejected ◀──── failing report ──── Deployable ⇄ Deployed
//
// Retry only what retrying can fix (D6): every infrastructure failure is
// returned as an error, which controller-runtime requeues with exponential
// backoff; every verdict on the image is Rejected, terminal (D5). A status
// write triggers the next reconcile through the watch, so transitions
// return an empty Result.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mspv1 "github.com/yschiang/msp/msp/api/v1"
	"github.com/yschiang/msp/msp/internal/conformance"
	"github.com/yschiang/msp/msp/internal/manifest"
)

// DefaultClusterNamespaces is the D14 map; a flag overrides it in main.
var DefaultClusterNamespaces = map[string]string{"blue": "msp-blue", "green": "msp-green"}

// requeueWhileRunning is how often a ConformanceRunning object polls its
// background run. ponytail: polling, not a completion channel wired into the
// work queue; a minute-long run polled every 5s is fine.
const requeueWhileRunning = 5 * time.Second

// shutdownGrace bounds how long shutdown waits for cancelled conformance runs
// to clean up. Cancellation makes them give up at the next docker call, so the
// wait is short in practice.
const shutdownGrace = 60 * time.Second

type Reconciler struct {
	client.Client
	ClusterNamespaces map[string]string
	Registries        Registries

	// The four things that touch the outside world, as fields so the fake
	// client tests can replace them (D21). main wires the real ones.
	ResolveDigest   func(ctx context.Context, ref string) (string, error)
	ExtractManifest func(ctx context.Context, ref string) ([]byte, error)
	Verify          func(ctx context.Context, ref string) (*conformance.Report, error)
	CopyImage       func(ctx context.Context, src, dst string) (string, error)

	runs      runTable
	manifests sync.Map // digest -> *manifest.Manifest; the D13 cache
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// A conformance run outlives the reconcile that started it, so it also
	// outlives mgr.Start unless someone waits: the process would exit with a
	// model container, a probe container and .msp-conform-tmp/<runID> behind
	// it, none of them labelled, none of them anyone else's to clean up. This
	// runnable holds mgr.Start open until the cancelled runs have cleaned up.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		<-ctx.Done()
		if !r.runs.stop(shutdownGrace) {
			return fmt.Errorf("conformance runs still in flight after %s; docker containers may be stranded", shutdownGrace)
		}
		return nil
	})); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&mspv1.ModelDeployment{}).
		Owns(&appsv1.Deployment{}). // ready-replica changes drive Deployable ⇄ Deployed (D4)
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	md := &mspv1.ModelDeployment{}
	if err := r.Get(ctx, req.NamespacedName, md); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch md.Status.Phase {
	case "":
		return r.declared(ctx, md)
	case mspv1.PhaseSyncing:
		return r.syncing(ctx, md)
	case mspv1.PhaseConformanceRunning:
		return r.conformanceRunning(ctx, md)
	case mspv1.PhaseDeployable, mspv1.PhaseDeployed:
		return r.deploy(ctx, md)
	default: // Rejected: terminal (D5)
		return ctrl.Result{}, nil
	}
}

func (r *Reconciler) setPhase(ctx context.Context, md *mspv1.ModelDeployment, phase, message string) (ctrl.Result, error) {
	md.Status.Phase = phase
	md.Status.Message = message
	if err := r.Status().Update(ctx, md); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) declared(ctx context.Context, md *mspv1.ModelDeployment) (ctrl.Result, error) {
	want, ok := r.ClusterNamespaces[md.Spec.Cluster]
	if !ok || want != md.Namespace {
		return r.setPhase(ctx, md, mspv1.PhaseRejected, fmt.Sprintf(
			"spec.cluster %q maps to namespace %q, but the object is in namespace %q", md.Spec.Cluster, want, md.Namespace))
	}
	return r.setPhase(ctx, md, mspv1.PhaseSyncing, "")
}

func (r *Reconciler) syncing(ctx context.Context, md *mspv1.ModelDeployment) (ctrl.Result, error) {
	ref := &md.Spec.ModelRef
	if ref.Digest == "" {
		digest, err := r.ResolveDigest(ctx, r.Registries.SourceRef(ref.Model, ref.Version))
		if err != nil {
			return ctrl.Result{}, err // registry unreachable: backoff (D6)
		}
		ref.Digest = digest
		if err := r.Update(ctx, md); err != nil {
			return ctrl.Result{}, err
		}
	}
	if md.Status.PinnedDigest == "" {
		md.Status.PinnedDigest = ref.Digest
		if err := r.Status().Update(ctx, md); err != nil {
			return ctrl.Result{}, err
		}
	}
	// From here the tag is dead to the platform: every reference is @digest.
	digest := md.Status.PinnedDigest
	m, reason, err := r.manifestFor(ctx, digest, r.Registries.PinnedSourceRef(ref.Model, digest))
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason != "" {
		return r.setPhase(ctx, md, mspv1.PhaseRejected, reason)
	}
	if m.Model.Name != ref.Model || m.Model.Version != ref.Version {
		return r.setPhase(ctx, md, mspv1.PhaseRejected, fmt.Sprintf(
			"spec.modelRef %s/%s does not match the image manifest's model %s/%s (D9)",
			ref.Model, ref.Version, m.Model.Name, m.Model.Version))
	}
	return r.setPhase(ctx, md, mspv1.PhaseConformanceRunning, "")
}

// manifestFor returns the manifest of the image at ref, cached by digest
// (D13). A missing or schema-invalid manifest is a verdict on the image and
// comes back as reason (the same verdict msp-conform reaches as C1); err is
// infrastructure only.
//
// The caller picks ref because the two callers read different registries for
// the same digest: syncing has to read model-center, the copy has not
// happened yet; deploy reads the internal copy, which is byte-identical and
// ours. A Deployed object whose cache went cold in a restart must not depend
// on model-center still holding an image it may have deleted or retagged —
// that is the drift the D8 copy exists to insulate against, and re-extracting
// upstream would turn it into permanent backoff.
func (r *Reconciler) manifestFor(ctx context.Context, digest, ref string) (*manifest.Manifest, string, error) {
	if v, ok := r.manifests.Load(digest); ok {
		return v.(*manifest.Manifest), "", nil
	}
	raw, err := r.ExtractManifest(ctx, ref)
	if errors.Is(err, conformance.ErrNoManifest) {
		return nil, err.Error(), nil
	}
	if err != nil {
		return nil, "", err
	}
	m, err := manifest.Load(raw)
	if err != nil {
		return nil, err.Error(), nil
	}
	r.manifests.Store(digest, m)
	return m, "", nil
}

func (r *Reconciler) conformanceRunning(ctx context.Context, md *mspv1.ModelDeployment) (ctrl.Result, error) {
	digest := md.Status.PinnedDigest
	ref := r.Registries.PinnedSourceRef(md.Spec.ModelRef.Model, digest)
	run := r.runs.start(digest, func(runCtx context.Context) (*conformance.Report, error) {
		// runCtx, not this reconcile's ctx: the reconcile that starts the run
		// returns long before the run finishes (D12). It is the run table's
		// own context, cancelled at shutdown so VerifyImage stops and its
		// deferred cleanup removes the containers it created.
		return r.Verify(runCtx, ref)
	})
	if !run.finished() {
		return ctrl.Result{RequeueAfter: requeueWhileRunning}, nil
	}
	if run.err != nil {
		r.runs.forget(digest) // so the next reconcile starts a fresh run
		return ctrl.Result{}, fmt.Errorf("conformance %s: %w", digest, run.err)
	}

	md.Status.Conformance = conformanceStatus(run.report)
	if !run.report.Pass {
		msg := "conformance failed: " + strings.Join(md.Status.Conformance.Failed, ", ")
		if missing := run.report.MissingChecks(); len(missing) > 0 {
			msg += "; not run: " + strings.Join(missing, ", ")
		}
		return r.setPhase(ctx, md, mspv1.PhaseRejected, msg)
	}

	got, err := r.CopyImage(ctx, ref, r.Registries.InternalRef(md.Spec.ModelRef.Model, digest))
	if err != nil {
		return ctrl.Result{}, err // internal registry unreachable: backoff (D6)
	}
	if got != digest {
		return r.setPhase(ctx, md, mspv1.PhaseRejected, fmt.Sprintf(
			"internal registry reports digest %s after copy, but the pinned digest is %s; refusing to deploy an unverified identity (D8)", got, digest))
	}
	if _, err := r.setPhase(ctx, md, mspv1.PhaseDeployable, ""); err != nil {
		return ctrl.Result{}, err
	}
	// Roll out in the same pass: the phase write alone changes nothing in the
	// cluster, and deploy() is idempotent.
	return r.deploy(ctx, md)
}

func conformanceStatus(rep *conformance.Report) mspv1.ConformanceStatus {
	var s mspv1.ConformanceStatus
	for _, c := range rep.Checks {
		if c.Pass {
			s.Passed = append(s.Passed, c.ID)
		} else {
			s.Failed = append(s.Failed, c.ID)
		}
	}
	return s
}

func (r *Reconciler) deploy(ctx context.Context, md *mspv1.ModelDeployment) (ctrl.Result, error) {
	// Cache miss after a restart re-extracts — from our own copy (D13, D8).
	image := r.Registries.InternalRef(md.Spec.ModelRef.Model, md.Status.PinnedDigest)
	m, reason, err := r.manifestFor(ctx, md.Status.PinnedDigest, image)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason != "" { // cannot happen for a digest that reached Deployable; a guard, not a path
		return r.setPhase(ctx, md, mspv1.PhaseRejected, reason)
	}
	dep, err := r.applyChildren(ctx, md, m, image)
	if err != nil {
		return ctrl.Result{}, err
	}
	phase := mspv1.PhaseDeployable
	if dep.Status.ReadyReplicas >= md.Spec.Replicas.Min {
		phase = mspv1.PhaseDeployed
	}
	if phase != md.Status.Phase { // D4: status describes the present
		return r.setPhase(ctx, md, phase, "")
	}
	return ctrl.Result{}, nil
}
