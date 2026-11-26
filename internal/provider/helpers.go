package provider

import (
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

// effectiveTopologyType returns the Instance's configured topology type, or
// the provider's default topology when spec.topology is omitted entirely.
// Group-replication is the default because it needs no orchestrator.
func effectiveTopologyType(inst *corev1alpha1.Instance) string {
	if t := inst.GetTopologyType(); t != "" {
		return t
	}
	return common.TopologyGroupReplication
}

// allowAnyLayout turns off the operator's opinions on what a production
// cluster looks like, so any layout the Instance asks for is accepted, such
// as a single node with a single proxy for development. These flags only
// relax the operator's CRD validation: MySQL size (async >= 2,
// group-replication odd and 3..8), Router size >= 2, Orchestrator size odd
// and >= 3, and async without backups, where a failover cannot recover the
// transactions stranded on the old primary.
//
// unsafeFlags.proxy and unsafeFlags.orchestrator are set by applyProxy and
// applyOrchestrator instead: the operator reads them to decide what to run.
func allowAnyLayout(cr *psv1.PerconaServerMySQL) {
	cr.Spec.Unsafe.MySQLSize = true
	cr.Spec.Unsafe.ProxySize = true
	cr.Spec.Unsafe.OrchestratorSize = true
	cr.Spec.Unsafe.AsyncFailoverWithPossibleDataLoss = true
}

func imageForComponentType(spec *corev1alpha1.ProviderSpec, componentType, version, override string) string {
	if override != "" {
		return override
	}
	if spec == nil {
		return ""
	}
	ct, ok := spec.ComponentTypes[componentType]
	if !ok {
		return ""
	}
	if version != "" {
		for _, v := range ct.Versions {
			if v.Version == version {
				return v.Image
			}
		}
	}
	if ct.DefaultVersion != "" {
		for _, v := range ct.Versions {
			if v.Version == ct.DefaultVersion && v.Image != "" {
				return v.Image
			}
		}
	}
	for _, v := range ct.Versions {
		if v.Image != "" {
			return v.Image
		}
	}
	return ""
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

func componentPodSpec(comp corev1alpha1.ComponentSpec, image string, defaultSize int32) psv1.PodSpec {
	size := defaultSize
	if comp.Replicas != nil {
		size = *comp.Replicas
	}
	ps := psv1.PodSpec{
		Size: size,
		ContainerSpec: psv1.ContainerSpec{
			Image: image,
		},
	}
	if comp.Resources != nil {
		ps.Resources = *comp.Resources
	}
	applySchedulingPolicy(&ps, comp.SchedulingPolicy)
	return ps
}

// applySchedulingPolicy copies OpenEverest placement settings onto a
// PerconaServerMySQL pod spec. Affinity is nested under PodAffinity.Advanced
// because the operator reserves the top-level affinity object for its own
// anti-affinity defaults.
func applySchedulingPolicy(ps *psv1.PodSpec, sp *commonv1alpha1.SchedulingPolicy) {
	if sp == nil {
		return
	}
	if sp.Affinity != nil {
		ps.Affinity = &psv1.PodAffinity{Advanced: sp.Affinity}
	}
	ps.NodeSelector = sp.NodeSelector
	ps.Tolerations = sp.Tolerations
	if sp.TopologySpreadConstraints != nil {
		ps.TopologySpreadConstraints = *sp.TopologySpreadConstraints
	}
	ps.SchedulerName = sp.SchedulerName
}
