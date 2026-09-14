package provider

import (
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

func TestValidateProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inst    *corev1alpha1.Instance
		wantErr bool
	}{
		{
			name: "absent is ok",
			inst: instanceWithTopology(common.TopologyAsync, nil),
		},
		{
			name: "haproxy on async",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentProxy: {Type: common.ProxyTypeHAProxy, Replicas: ptr32(2)},
			}),
		},
		{
			name: "empty type on async defaults to haproxy",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentProxy: {},
			}),
		},
		{
			name: "router on async rejected",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentProxy: {Type: common.ProxyTypeRouter},
			}),
			wantErr: true,
		},
		{
			name: "router on group-replication",
			inst: instanceWithTopology(common.TopologyGroupReplication, map[string]corev1alpha1.ComponentSpec{
				common.ComponentProxy: {Type: common.ProxyTypeRouter, Replicas: ptr32(2)},
			}),
		},
		{
			name: "haproxy on group-replication",
			inst: instanceWithTopology(common.TopologyGroupReplication, map[string]corev1alpha1.ComponentSpec{
				common.ComponentProxy: {Type: common.ProxyTypeHAProxy},
			}),
		},
		{
			name: "router size 1 on group-replication rejected",
			inst: instanceWithTopology(common.TopologyGroupReplication, map[string]corev1alpha1.ComponentSpec{
				common.ComponentProxy: {Type: common.ProxyTypeRouter, Replicas: ptr32(1)},
			}),
			wantErr: true,
		},
		{
			name: "unknown type rejected",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentProxy: {Type: "proxysql"},
			}),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateProxy(tt.inst)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestApplyProxy(t *testing.T) {
	t.Parallel()

	spec := &corev1alpha1.ProviderSpec{
		Components: map[string]corev1alpha1.Component{
			common.ComponentProxy: {Type: common.ProxyTypeHAProxy},
		},
		ComponentTypes: map[string]corev1alpha1.ComponentType{
			common.ProxyTypeHAProxy: {
				Versions: []corev1alpha1.ComponentVersion{
					{Version: "2.8.18-1", Image: "percona/haproxy:2.8.18-1", Default: true},
				},
			},
			common.ProxyTypeRouter: {
				Versions: []corev1alpha1.ComponentVersion{
					{Version: "8.4.10", Image: "percona/percona-mysql-router:8.4.10", Default: true},
				},
			},
		},
	}

	t.Run("disabled on async sets unsafe flag", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		inst := instanceWithTopology(common.TopologyAsync, nil)
		if err := applyProxy(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		if cr.Spec.Proxy.HAProxy.Enabled || cr.Spec.Proxy.Router.Enabled {
			t.Fatal("expected proxy disabled")
		}
		if !cr.Spec.Unsafe.Proxy {
			t.Fatal("expected unsafeFlags.proxy")
		}
	})

	t.Run("haproxy on async", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		inst := instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
			common.ComponentProxy: {
				Type:     common.ProxyTypeHAProxy,
				Replicas: ptr32(3),
				Service:  &corev1alpha1.Service{ServiceType: corev1.ServiceTypeLoadBalancer},
			},
		})
		if err := applyProxy(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		if !cr.Spec.Proxy.HAProxy.Enabled {
			t.Fatal("haproxy should be enabled")
		}
		if cr.Spec.Proxy.Router.Enabled {
			t.Fatal("router must be off when haproxy is on")
		}
		if cr.Spec.Proxy.HAProxy.Size != 3 {
			t.Fatalf("size: got %d", cr.Spec.Proxy.HAProxy.Size)
		}
		if cr.Spec.Proxy.HAProxy.Image != "percona/haproxy:2.8.18-1" {
			t.Fatalf("image: got %q", cr.Spec.Proxy.HAProxy.Image)
		}
		if cr.Spec.Proxy.HAProxy.Expose.Type != corev1.ServiceTypeLoadBalancer {
			t.Fatalf("expose: got %q", cr.Spec.Proxy.HAProxy.Expose.Type)
		}
		if cr.Spec.Unsafe.Proxy {
			t.Fatal("unsafe flag should be cleared")
		}
	})

	t.Run("router on group-replication", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		inst := instanceWithTopology(common.TopologyGroupReplication, map[string]corev1alpha1.ComponentSpec{
			common.ComponentProxy: {Type: common.ProxyTypeRouter},
		})
		if err := applyProxy(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		if !cr.Spec.Proxy.Router.Enabled {
			t.Fatal("router should be enabled")
		}
		if cr.Spec.Proxy.HAProxy.Enabled {
			t.Fatal("haproxy must be off when router is on")
		}
		if cr.Spec.Proxy.Router.Size != defaultProxySize {
			t.Fatalf("size: got %d", cr.Spec.Proxy.Router.Size)
		}
		if cr.Spec.Proxy.Router.Image != "percona/percona-mysql-router:8.4.10" {
			t.Fatalf("image: got %q", cr.Spec.Proxy.Router.Image)
		}
	})

	t.Run("empty type on group-replication defaults to router", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		inst := instanceWithTopology(common.TopologyGroupReplication, map[string]corev1alpha1.ComponentSpec{
			common.ComponentProxy: {},
		})
		if err := applyProxy(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		if !cr.Spec.Proxy.Router.Enabled || cr.Spec.Proxy.HAProxy.Enabled {
			t.Fatal("expected router default on group-replication")
		}
	})
}
