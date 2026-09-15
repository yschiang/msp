package syncctl

// This file: the native objects a ModelDeployment owns — Deployment, Service,
// HPA — built from the image's own manifest plus the CR (design D15–D19), and
// the apply step that reconciles them (D15: replicas written once; D16:
// everything else driven back to desired state; D19: ownerReferences, so
// deletion is garbage collection).

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mspv1 "github.com/yschiang/msp/msp/api/v1"
	"github.com/yschiang/msp/msp/internal/manifest"
)

const (
	LabelDeployment = "msp.platform/deployment"
	LabelModel      = "msp.platform/model"
	LabelVersion    = "msp.platform/version"
	// InflightMetric is the only HPA metric (spec §5.2): per-pod in-flight
	// requests via a Prometheus adapter. Absent locally, so the HPA is inert (D18).
	InflightMetric = "msp_inference_inflight"
	// ponytail: the spec names the metric but no target; 10 in-flight per pod
	// is a placeholder until the metrics stack (Phase 4) gives a real number.
	inflightTargetPerPod = 10
)

func childLabels(md *mspv1.ModelDeployment) map[string]string {
	return map[string]string{
		LabelDeployment: md.Name,
		LabelModel:      md.Spec.ModelRef.Model,
		LabelVersion:    md.Spec.ModelRef.Version,
	}
}

func resourceList(in map[string]string) (corev1.ResourceList, error) {
	out := corev1.ResourceList{}
	for k, v := range in {
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return nil, fmt.Errorf("manifest resource %s=%q: %w", k, v, err)
		}
		out[corev1.ResourceName(k)] = q
	}
	return out, nil
}

// BuildDeployment is the desired Deployment: image is the internal-registry
// digest reference; resources, port and startup budget come from the manifest
// unmodified (no override field exists — SPEC-GAP, design §4).
func BuildDeployment(md *mspv1.ModelDeployment, m *manifest.Manifest, image string) (*appsv1.Deployment, error) {
	requests, err := resourceList(m.Runtime.Resources.Requests)
	if err != nil {
		return nil, err
	}
	limits, err := resourceList(m.Runtime.Resources.Limits)
	if err != nil {
		return nil, err
	}
	port := int32(m.Contract.Port)
	grpcProbe := func(period, failures int32) *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler:     corev1.ProbeHandler{GRPC: &corev1.GRPCAction{Port: port}},
			PeriodSeconds:    period,
			FailureThreshold: failures,
		}
	}
	replicas := md.Spec.Replicas.Min
	labels := childLabels(md)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: md.Name, Namespace: md.Namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{LabelDeployment: md.Name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "model",
					Image: image,
					Ports: []corev1.ContainerPort{{Name: "grpc", ContainerPort: port}},
					// Closes Phase 0's model_digest stand-in: the SDK reads
					// MSP_MODEL_DIGEST and echoes it in every PredictResponse.
					Env:       []corev1.EnvVar{{Name: "MSP_MODEL_DIGEST", Value: md.Status.PinnedDigest}},
					Resources: corev1.ResourceRequirements{Requests: requests, Limits: limits},
					// D17: the startup budget is the manifest's startupSeconds,
					// the same number conformance C2 enforces. No liveness probe.
					StartupProbe:   grpcProbe(1, int32(m.Runtime.StartupSeconds)),
					ReadinessProbe: grpcProbe(5, 3),
				}}},
			},
		},
	}, nil
}

func BuildService(md *mspv1.ModelDeployment, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: md.Name, Namespace: md.Namespace, Labels: childLabels(md)},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{LabelDeployment: md.Name},
			Ports:    []corev1.ServicePort{{Name: "grpc", Port: port, TargetPort: intstr.FromInt32(port)}},
		},
	}
}

func BuildHPA(md *mspv1.ModelDeployment) *autoscalingv2.HorizontalPodAutoscaler {
	min := md.Spec.Replicas.Min
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: md.Name, Namespace: md.Namespace, Labels: childLabels(md)},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: md.Name},
			MinReplicas:    &min,
			MaxReplicas:    md.Spec.Replicas.Max,
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.PodsMetricSourceType,
				Pods: &autoscalingv2.PodsMetricSource{
					Metric: autoscalingv2.MetricIdentifier{Name: InflightMetric},
					Target: autoscalingv2.MetricTarget{
						Type:         autoscalingv2.AverageValueMetricType,
						AverageValue: resource.NewQuantity(inflightTargetPerPod, resource.DecimalSI),
					},
				},
			}},
		},
	}
}

// applyChildren creates or updates the three owned objects and returns the
// live Deployment (whose status drives Deployable ⇄ Deployed).
func (r *Reconciler) applyChildren(ctx context.Context, md *mspv1.ModelDeployment, m *manifest.Manifest, image string) (*appsv1.Deployment, error) {
	wantDep, err := BuildDeployment(md, m, image)
	if err != nil {
		return nil, err
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: md.Name, Namespace: md.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		// D15: replicas is written exactly once, at creation (ResourceVersion
		// is empty only for an object that does not exist yet). After that
		// the HPA owns it.
		if dep.ResourceVersion == "" {
			dep.Spec.Replicas = wantDep.Spec.Replicas
		}
		// D16: everything else is driven back to desired state every time.
		dep.Labels = wantDep.Labels
		dep.Spec.Selector = wantDep.Spec.Selector
		dep.Spec.Template = wantDep.Spec.Template
		return controllerutil.SetControllerReference(md, dep, r.Scheme())
	}); err != nil {
		return nil, fmt.Errorf("deployment: %w", err)
	}

	wantSvc := BuildService(md, int32(m.Contract.Port))
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: md.Name, Namespace: md.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = wantSvc.Labels
		svc.Spec.Selector = wantSvc.Spec.Selector
		svc.Spec.Ports = wantSvc.Spec.Ports // clusterIP etc. are left to the API server
		return controllerutil.SetControllerReference(md, svc, r.Scheme())
	}); err != nil {
		return nil, fmt.Errorf("service: %w", err)
	}

	wantHPA := BuildHPA(md)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: md.Name, Namespace: md.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, hpa, func() error {
		hpa.Labels = wantHPA.Labels
		hpa.Spec = wantHPA.Spec
		return controllerutil.SetControllerReference(md, hpa, r.Scheme())
	}); err != nil {
		return nil, fmt.Errorf("hpa: %w", err)
	}
	return dep, nil
}
