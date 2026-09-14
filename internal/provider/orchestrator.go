package provider

import (
	"fmt"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const defaultOrchestratorSize int32 = 3

// validateOrchestrator mirrors the operator's OrchestratorEnabled() rules:
//   - group-replication: orchestrator is ignored (never used)
//   - async: the component may be omitted only because applyOrchestrator will
//     set unsafeFlags.orchestrator; when present, size must be odd and >= 3
func validateOrchestrator(inst *corev1alpha1.Instance) error {
	if inst.GetTopologyType() == common.TopologyGroupReplication {
		return nil
	}

	orch, enabled := inst.Spec.Components[common.ComponentOrchestrator]
	if !enabled {
		return nil
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

// applyOrchestrator maps Instance.spec.components.orchestrator onto the operator CR
// the same way PerconaServerMySQL.OrchestratorEnabled() interprets the CR:
//
//	if GR: always off (component is ignored)
//	if async && !unsafe.Orchestrator: always on
//	else: Spec.Orchestrator.Enabled
//
// Omitting the component on async is how the Instance API requests disable, so
// we set unsafeFlags.orchestrator. Image comes from the version catalog unless
// the user overrides ComponentSpec.Image.
func applyOrchestrator(cr *psv1.PerconaServerMySQL, inst *corev1alpha1.Instance, spec *corev1alpha1.ProviderSpec) error {
	if inst.GetTopologyType() == common.TopologyGroupReplication {
		cr.Spec.Orchestrator = psv1.OrchestratorSpec{Enabled: false}
		cr.Spec.Unsafe.Orchestrator = false
		return nil
	}

	orch, enabled := inst.Spec.Components[common.ComponentOrchestrator]
	if !enabled {
		// Async without orchestrator is only legal with the unsafe flag.
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
