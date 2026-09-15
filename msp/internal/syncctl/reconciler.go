package syncctl

import "sigs.k8s.io/controller-runtime/pkg/client"

// Reconciler is the Sync Controller. Filled in by the reconciler task.
type Reconciler struct {
	client.Client
}
