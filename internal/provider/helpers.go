package provider

import (
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

func mysqlSizeRequiresUnsafe(topology string, size int32) bool {
	switch topology {
	case common.TopologyAsync:
		return size < psv1.MinSafeAsyncSize
	case common.TopologyGroupReplication:
		return size < psv1.MinSafeGRSize || size >= psv1.MaxSafeGRSize || size%2 == 0
	default:
		return false
	}
}

// orchestratorSizeRequiresUnsafe reports whether an enabled orchestrator's
// size falls outside the operator's safe range for async replication: the
// operator requires the size to be odd and >= 3 (psv1.MinSafeGRSize) unless
// unsafeFlags.orchestratorSize is set.
func orchestratorSizeRequiresUnsafe(size int32) bool {
	return size < psv1.MinSafeGRSize || size%2 == 0
}

// proxySizeRequiresUnsafe reports whether an enabled MySQL Router's size
// falls below the operator's safe minimum for group-replication
// (psv1.MinSafeProxySize) unless unsafeFlags.proxySize is set. HAProxy has
// no equivalent minimum in the operator, so it always returns false.
func proxySizeRequiresUnsafe(proxyType string, size int32) bool {
	if proxyType != common.ProxyTypeRouter {
		return false
	}
	return size < psv1.MinSafeProxySize
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
	if sp := comp.SchedulingPolicy; sp != nil {
		if sp.Affinity != nil {
			ps.Affinity = &psv1.PodAffinity{Advanced: sp.Affinity}
		}
		ps.NodeSelector = sp.NodeSelector
		ps.Tolerations = sp.Tolerations
		ps.TopologySpreadConstraints = sp.TopologySpreadConstraints
		ps.SchedulerName = sp.SchedulerName
	}
	return ps
}
