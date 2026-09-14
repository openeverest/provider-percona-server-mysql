package provider

import (
	"fmt"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const defaultProxySize int32 = 2

func defaultProxyType(topology string) string {
	if topology == common.TopologyGroupReplication {
		return common.ProxyTypeRouter
	}
	return common.ProxyTypeHAProxy
}

func proxyTypeOf(comp corev1alpha1.ComponentSpec, topology string) string {
	if comp.Type != "" {
		return comp.Type
	}
	return defaultProxyType(topology)
}

// validateProxy enforces operator proxy rules:
//   - async: only haproxy
//   - group-replication: haproxy or router (router size >= 2)
//
// Omitting the component is allowed; applyProxy sets unsafeFlags.proxy.
func validateProxy(inst *corev1alpha1.Instance) error {
	proxy, enabled := inst.Spec.Components[common.ComponentProxy]
	if !enabled {
		return nil
	}

	topology := inst.GetTopologyType()
	proxyType := proxyTypeOf(proxy, topology)
	switch proxyType {
	case common.ProxyTypeHAProxy, common.ProxyTypeRouter:
	default:
		return fmt.Errorf("%q type must be %q or %q", common.ComponentProxy, common.ProxyTypeHAProxy, common.ProxyTypeRouter)
	}
	if topology == common.TopologyAsync && proxyType != common.ProxyTypeHAProxy {
		return fmt.Errorf("%q topology only supports proxy type %q", common.TopologyAsync, common.ProxyTypeHAProxy)
	}
	if proxy.Replicas != nil && *proxy.Replicas < 1 {
		return fmt.Errorf("%q replicas must be >= 1", common.ComponentProxy)
	}
	if topology == common.TopologyGroupReplication && proxyType == common.ProxyTypeRouter {
		size := defaultProxySize
		if proxy.Replicas != nil {
			size = *proxy.Replicas
		}
		if size < psv1.MinSafeProxySize {
			return fmt.Errorf("%q router replicas must be >= %d on %q", common.ComponentProxy, psv1.MinSafeProxySize, common.TopologyGroupReplication)
		}
	}
	return nil
}

// applyProxy maps Instance.spec.components.proxy onto the operator CR.
// HAProxy and Router cannot both be enabled. Omitting the component on async
// or group-replication sets unsafeFlags.proxy so the operator accepts no proxy.
func applyProxy(cr *psv1.PerconaServerMySQL, inst *corev1alpha1.Instance, spec *corev1alpha1.ProviderSpec) error {
	proxy, enabled := inst.Spec.Components[common.ComponentProxy]
	if !enabled {
		cr.Spec.Proxy = psv1.ProxySpec{
			HAProxy: &psv1.HAProxySpec{Enabled: false},
			Router:  &psv1.MySQLRouterSpec{Enabled: false},
		}
		switch inst.GetTopologyType() {
		case common.TopologyAsync, common.TopologyGroupReplication:
			cr.Spec.Unsafe.Proxy = true
		}
		return nil
	}

	topology := inst.GetTopologyType()
	proxyType := proxyTypeOf(proxy, topology)
	image := imageForComponentType(spec, proxyType, proxy.Version, proxy.Image)
	if image == "" {
		return fmt.Errorf("cannot resolve image for %q type %q", common.ComponentProxy, proxyType)
	}

	pod := componentPodSpec(proxy, image, defaultProxySize)
	var expose psv1.ServiceExpose
	if proxy.Service != nil {
		expose = serviceExposeFromComponent(proxy.Service)
	}

	cr.Spec.Unsafe.Proxy = false
	switch proxyType {
	case common.ProxyTypeHAProxy:
		cr.Spec.Proxy.HAProxy = &psv1.HAProxySpec{Enabled: true, Expose: expose, PodSpec: pod}
		cr.Spec.Proxy.Router = &psv1.MySQLRouterSpec{Enabled: false}
	case common.ProxyTypeRouter:
		cr.Spec.Proxy.Router = &psv1.MySQLRouterSpec{Enabled: true, Expose: expose, PodSpec: pod}
		cr.Spec.Proxy.HAProxy = &psv1.HAProxySpec{Enabled: false}
	default:
		return fmt.Errorf("unsupported proxy type %q", proxyType)
	}
	return nil
}
