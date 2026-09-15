// msp-sync is the Phase 1 Sync Controller (MSP-SPEC-001 §5): it runs
// out-of-cluster on the developer's host (design D3), watches ModelDeployment
// objects, and drives the host's Docker daemon for conformance. Flags
// override the two hardcoded maps (D14): cluster → namespace, and the two
// registry addresses.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mspv1 "github.com/yschiang/msp/msp/api/v1"
	"github.com/yschiang/msp/msp/internal/conformance"
	"github.com/yschiang/msp/msp/internal/syncctl"
)

func main() {
	modelCenter := flag.String("model-center-registry", "localhost:5010", "host:port of the model-center registry (spec.modelRef.source=model-center)")
	internal := flag.String("internal-registry", "localhost:5011", "host:port of the platform-internal registry pods pull from")
	clusterMap := flag.String("cluster-namespaces", "blue=msp-blue,green=msp-green", "spec.cluster=namespace pairs, comma-separated")
	ctrl.RegisterFlags(flag.CommandLine) // --kubeconfig
	flag.Parse()
	ctrl.SetLogger(zap.New())

	namespaces, err := parsePairs(*clusterMap)
	if err != nil {
		fmt.Fprintln(os.Stderr, "msp-sync:", err)
		os.Exit(2)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := mspv1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	// Room for the controller's own shutdown runnable, which waits up to 60s
	// for cancelled conformance runs to remove their docker containers; the
	// manager's default 30s would cut that wait short.
	grace := 90 * time.Second
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		// ponytail: no metrics endpoint; the default :8080 is the model port.
		Metrics:                 metricsserver.Options{BindAddress: "0"},
		GracefulShutdownTimeout: &grace,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "msp-sync: manager:", err)
		os.Exit(2)
	}

	r := &syncctl.Reconciler{
		Client:            mgr.GetClient(),
		ClusterNamespaces: namespaces,
		Registries:        syncctl.Registries{ModelCenter: *modelCenter, Internal: *internal},
		ResolveDigest:     syncctl.ResolveDigest,
		ExtractManifest:   conformance.ExtractManifest,
		Verify: func(ctx context.Context, ref string) (*conformance.Report, error) {
			return conformance.VerifyImage(ctx, ref, conformance.VerifyOptions{})
		},
		CopyImage: syncctl.CopyImage,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		fmt.Fprintln(os.Stderr, "msp-sync: controller:", err)
		os.Exit(2)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintln(os.Stderr, "msp-sync:", err)
		os.Exit(1)
	}
}

// parsePairs turns "blue=msp-blue,green=msp-green" into a map.
func parsePairs(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("--cluster-namespaces: bad entry %q (want cluster=namespace)", kv)
		}
		out[k] = v
	}
	return out, nil
}
