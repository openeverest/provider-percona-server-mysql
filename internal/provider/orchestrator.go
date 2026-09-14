package provider

import (
	"fmt"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const defaultOrchestratorSize int32 = 3

// validateOrchestrator checks the optional orchestrator component.
// Presence of spec.components.orchestrator is the enable flag; size/image/resources
// come from the standard ComponentSpec fields, not from PodSpec.
func validateOrchestrator(inst *corev1alpha1.Instance) error {
	orch, enabled := inst.Spec.Components[common.ComponentOrchestrator]
	if !enabled {
		return nil
	}

	if inst.GetTopologyType() == common.TopologyGroupReplication {
		return fmt.Errorf("%q is only supported on the %q topology", common.ComponentOrchestrator, common.TopologyAsync)
	}
	if orch.Type != "" && orch.Type != common.ComponentTypeOrchestrator {
		return fmt.Errorf("%q component type must be %q", common.ComponentOrchestrator, common.ComponentTypeOrchestrator)
	}
	if orch.Replicas != nil {
		if *orch.Replicas < 1 {
			return fmt.Errorf("%q replicas must be >= 1", common.ComponentOrchestrator)
		}
		if *orch.Replicas < 3 || *orch.Replicas%2 == 0 {
			return fmt.Errorf("%q replicas must be odd and >= 3 (got %d)", common.ComponentOrchestrator, *orch.Replicas)
		}
	}
	return nil
}

// applyOrchestrator maps Instance.spec.components.orchestrator onto the operator CR.
// Disabled-on-async sets unsafeFlags.orchestrator so the operator accepts async
// without Orchestrator. Image is resolved from the version catalog unless overridden.
func applyOrchestrator(cr *psv1.PerconaServerMySQL, inst *corev1alpha1.Instance, spec *corev1alpha1.ProviderSpec) error {
	orch, enabled := inst.Spec.Components[common.ComponentOrchestrator]
	if !enabled {
		cr.Spec.Orchestrator = psv1.OrchestratorSpec{Enabled: false}
		if inst.GetTopologyType() == common.TopologyAsync {
			cr.Spec.Unsafe.Orchestrator = true
		}
		return nil
	}

	image := orchestratorImage(orch, spec)
	if image == "" {
		return fmt.Errorf("cannot resolve image for %q component", common.ComponentOrchestrator)
	}

	size := defaultOrchestratorSize
	if orch.Replicas != nil {
		size = *orch.Replicas
	}

	out := psv1.OrchestratorSpec{
		Enabled: true,
		PodSpec: psv1.PodSpec{
			Size: size,
			ContainerSpec: psv1.ContainerSpec{
				Image: image,
			},
		},
	}
	if orch.Resources != nil {
		out.Resources = *orch.Resources
	}
	if orch.Affinity != nil {
		out.Affinity = &psv1.PodAffinity{Advanced: orch.Affinity}
	}
	if orch.Service != nil {
		out.Expose = serviceExposeFromComponent(orch.Service)
	}

	cr.Spec.Orchestrator = out
	cr.Spec.Unsafe.Orchestrator = false
	return nil
}

func orchestratorImage(comp corev1alpha1.ComponentSpec, spec *corev1alpha1.ProviderSpec) string {
	if comp.Image != "" {
		return comp.Image
	}
	if spec == nil {
		return ""
	}
	if comp.Version != "" {
		if image := controller.GetImageForVersion(spec, common.ComponentOrchestrator, comp.Version); image != "" {
			return image
		}
	}
	return controller.GetDefaultImageForComponent(spec, common.ComponentOrchestrator)
}

func serviceExposeFromComponent(svc *corev1alpha1.Service) psv1.ServiceExpose {
	expose := psv1.ServiceExpose{
		Type:        svc.ServiceType,
		Annotations: svc.Annotations,
	}
	if svc.LoadBalancerService != nil {
		ranges := svc.LoadBalancerService.SourceRanges.NormalizedSourceRanges()
		if len(ranges) > 0 {
			expose.LoadBalancerSourceRanges = []string(ranges)
		}
	}
	return expose
}
