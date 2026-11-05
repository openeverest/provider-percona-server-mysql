package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller/validationtest"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

const (
	providerSpecPath        = "../../charts/provider-percona-server-mysql/generated/provider-spec.yaml"
	asyncFixture            = "testdata/validate/async.yaml"
	groupReplicationFixture = "testdata/validate/groupreplication.yaml"
)

// component applies mutate to the named component of the Instance.
func component(name string, mutate func(*corev1alpha1.ComponentSpec)) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		c := in.Spec.Components[name]
		mutate(&c)
		in.Spec.Components[name] = c
	}
}

// proxyType sets the proxy component's type.
func proxyType(t string) func(*corev1alpha1.Instance) {
	return component(common.ComponentProxy, func(c *corev1alpha1.ComponentSpec) { c.Type = t })
}

// TestValidate runs the checks the runtime runs before every Sync: the
// schemas, supportedFields and catalog the provider publishes, then Validate.
func TestValidate(t *testing.T) {
	t.Parallel()
	v := validationtest.New(t, New(), providerSpecPath)

	tests := []struct {
		name    string
		fixture string
		mutate  func(*corev1alpha1.Instance)
		want    []string
	}{
		{name: "valid groupreplication with Router", fixture: groupReplicationFixture},
		{
			name:    "HAProxy on groupreplication",
			fixture: groupReplicationFixture,
			mutate:  proxyType(common.ProxyTypeHAProxy),
		},
		{
			name:    "proxy type unset on groupreplication",
			fixture: groupReplicationFixture,
			mutate:  proxyType(""),
		},
		{
			name:    "groupreplication without a proxy",
			fixture: groupReplicationFixture,
			mutate:  func(in *corev1alpha1.Instance) { delete(in.Spec.Components, common.ComponentProxy) },
		},
		{
			name:    "no topology type",
			fixture: groupReplicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology = nil
				proxyType("")(in)
			},
		},
		{
			name:    "proxy type outside the topology's choice",
			fixture: groupReplicationFixture,
			mutate:  proxyType("proxysql"),
			want:    []string{"spec.components[proxy].type"},
		},
		{
			name:    "storage on the groupreplication proxy",
			fixture: groupReplicationFixture,
			mutate: component(common.ComponentProxy, func(c *corev1alpha1.ComponentSpec) {
				c.Storage = &corev1alpha1.Storage{Size: resource.MustParse("1Gi")}
			}),
			want: []string{"spec.components[proxy].storage"},
		},
		{
			name:    "zero proxy replicas",
			fixture: groupReplicationFixture,
			mutate:  component(common.ComponentProxy, func(c *corev1alpha1.ComponentSpec) { c.Replicas = new(int32(0)) }),
			want:    []string{"spec.components[proxy].replicas"},
		},
		{
			name:    "zero proxy replicas without a topology type",
			fixture: groupReplicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology = nil
				component(common.ComponentProxy, func(c *corev1alpha1.ComponentSpec) {
					c.Type = ""
					c.Replicas = new(int32(0))
				})(in)
			},
			want: []string{"spec.components[proxy].replicas"},
		},
		{
			name:    "orchestrator on groupreplication",
			fixture: groupReplicationFixture,
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Components[common.ComponentOrchestrator] = corev1alpha1.ComponentSpec{Replicas: new(int32(3))}
			},
			want: []string{"spec.components[orchestrator]"},
		},
		{name: "valid async", fixture: asyncFixture},
		{
			name:    "async without orchestrator or proxy",
			fixture: asyncFixture,
			mutate: func(in *corev1alpha1.Instance) {
				delete(in.Spec.Components, common.ComponentOrchestrator)
				delete(in.Spec.Components, common.ComponentProxy)
			},
		},
		{
			name:    "Router on async",
			fixture: asyncFixture,
			mutate:  proxyType(common.ProxyTypeRouter),
			want:    []string{"spec.components[proxy].type"},
		},
		{
			name:    "zero orchestrator replicas",
			fixture: asyncFixture,
			mutate: component(common.ComponentOrchestrator, func(c *corev1alpha1.ComponentSpec) {
				c.Replicas = new(int32(0))
			}),
			want: []string{"spec.components[orchestrator].replicas"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := validationtest.Load(t, tt.fixture)
			if tt.mutate != nil {
				tt.mutate(in)
			}

			res := v.Run(in, nil)

			var got []string
			for _, err := range res.Errors {
				got = append(got, err.Field)
			}
			assert.Equal(t, tt.want, got, "errors: %v", res.Errors)
		})
	}
}
