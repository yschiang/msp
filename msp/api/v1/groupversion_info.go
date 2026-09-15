// Package v1 is the ModelDeployment API, group msp.platform (MSP-SPEC-001
// §5.2, Phase 1 design §4). Types carry controller-gen markers; the CRD YAML
// in deploy/platform/crd/ and zz_generated.deepcopy.go are generated from
// them by `make crd` and committed.
//
// +kubebuilder:object:generate=true
// +groupName=msp.platform
package v1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "msp.platform", Version: "v1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)
