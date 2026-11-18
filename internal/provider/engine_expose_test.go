package provider

import (
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestApplyEngineExpose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		service *corev1alpha1.Service
		want    psv1.ServiceExposeTogglable
	}{
		{
			name: "no service disables both primary and per-pod exposure",
			want: psv1.ServiceExposeTogglable{},
		},
		{
			name:    "ClusterIP enables both with the default type",
			service: &corev1alpha1.Service{ServiceType: corev1.ServiceTypeClusterIP},
			want: psv1.ServiceExposeTogglable{
				Enabled:       true,
				ServiceExpose: psv1.ServiceExpose{Type: corev1.ServiceTypeClusterIP},
			},
		},
		{
			name: "LoadBalancer with annotations and source ranges",
			service: &corev1alpha1.Service{
				ServiceType: corev1.ServiceTypeLoadBalancer,
				Annotations: map[string]string{"a": "b"},
				LoadBalancerService: &corev1alpha1.LoadBalancerService{
					SourceRanges: corev1alpha1.SourceRanges{"10.0.0.0/8"},
				},
			},
			want: psv1.ServiceExposeTogglable{
				Enabled: true,
				ServiceExpose: psv1.ServiceExpose{
					Type:                     corev1.ServiceTypeLoadBalancer,
					Annotations:              map[string]string{"a": "b"},
					LoadBalancerSourceRanges: []string{"10.0.0.0/8"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cluster := &psv1.PerconaServerMySQL{}
			applyEngineExpose(cluster, corev1alpha1.ComponentSpec{Service: tt.service})

			assert.Equal(t, tt.want, cluster.Spec.MySQL.ExposePrimary)
			assert.Equal(t, tt.want, cluster.Spec.MySQL.Expose)
		})
	}
}
