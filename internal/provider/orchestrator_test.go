package provider

import (
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

func TestValidateOrchestrator(t *testing.T) {
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
			name: "enabled on async with default size",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentOrchestrator: {},
			}),
		},
		{
			name: "enabled on async with odd size",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentOrchestrator: {Replicas: ptr32(5)},
			}),
		},
		{
			name: "even size rejected",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentOrchestrator: {Replicas: ptr32(2)},
			}),
			wantErr: true,
		},
		{
			name: "size 1 rejected",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentOrchestrator: {Replicas: ptr32(1)},
			}),
			wantErr: true,
		},
		{
			name: "wrong type rejected",
			inst: instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
				common.ComponentOrchestrator: {Type: "mysql"},
			}),
			wantErr: true,
		},
		{
			name: "orchestrator on group-replication is ignored",
			inst: instanceWithTopology(common.TopologyGroupReplication, map[string]corev1alpha1.ComponentSpec{
				common.ComponentOrchestrator: {Replicas: ptr32(2)},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateOrchestrator(tt.inst)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestApplyOrchestrator(t *testing.T) {
	t.Parallel()

	spec := &corev1alpha1.ProviderSpec{
		Components: map[string]corev1alpha1.Component{
			common.ComponentOrchestrator: {Type: common.ComponentTypeOrchestrator},
		},
		ComponentTypes: map[string]corev1alpha1.ComponentType{
			common.ComponentTypeOrchestrator: {
				Versions: []corev1alpha1.ComponentVersion{
					{Version: "3.2.6-22", Image: "percona/percona-orchestrator:3.2.6-22", Default: true},
					{Version: "3.2.6-21", Image: "percona/percona-orchestrator:3.2.6-21"},
				},
			},
		},
	}

	t.Run("disabled on async sets unsafe flag", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		inst := instanceWithTopology(common.TopologyAsync, nil)
		if err := applyOrchestrator(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		if cr.Spec.Orchestrator.Enabled {
			t.Fatal("expected orchestrator disabled")
		}
		if !cr.Spec.Unsafe.Orchestrator {
			t.Fatal("expected unsafeFlags.orchestrator for async without orchestrator")
		}
	})

	t.Run("disabled on group-replication does not set unsafe flag", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		inst := instanceWithTopology(common.TopologyGroupReplication, nil)
		if err := applyOrchestrator(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		if cr.Spec.Orchestrator.Enabled || cr.Spec.Unsafe.Orchestrator {
			t.Fatal("orchestrator should stay off without unsafe flags")
		}
	})

	t.Run("enabled maps size image resources service", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		cpu := resource.MustParse("200m")
		mem := resource.MustParse("256Mi")
		inst := instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
			common.ComponentOrchestrator: {
				Replicas: ptr32(5),
				Version:  "3.2.6-21",
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    cpu,
						corev1.ResourceMemory: mem,
					},
				},
				Service: &corev1alpha1.Service{ServiceType: corev1.ServiceTypeClusterIP},
			},
		})
		if err := applyOrchestrator(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		got := cr.Spec.Orchestrator
		if !got.Enabled {
			t.Fatal("expected enabled")
		}
		if got.Size != 5 {
			t.Fatalf("size: got %d", got.Size)
		}
		if got.Image != "percona/percona-orchestrator:3.2.6-21" {
			t.Fatalf("image: got %q", got.Image)
		}
		if got.Resources.Requests[corev1.ResourceCPU] != cpu {
			t.Fatalf("cpu: got %v", got.Resources.Requests[corev1.ResourceCPU])
		}
		if got.Expose.Type != corev1.ServiceTypeClusterIP {
			t.Fatalf("expose type: got %q", got.Expose.Type)
		}
		if cr.Spec.Unsafe.Orchestrator {
			t.Fatal("unsafe flag should be cleared when enabled")
		}
	})

	t.Run("explicit image override wins", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		inst := instanceWithTopology(common.TopologyAsync, map[string]corev1alpha1.ComponentSpec{
			common.ComponentOrchestrator: {Image: "example/orchestrator:dev"},
		})
		if err := applyOrchestrator(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		if cr.Spec.Orchestrator.Image != "example/orchestrator:dev" {
			t.Fatalf("image: got %q", cr.Spec.Orchestrator.Image)
		}
		if cr.Spec.Orchestrator.Size != defaultOrchestratorSize {
			t.Fatalf("default size: got %d", cr.Spec.Orchestrator.Size)
		}
	})

	t.Run("group-replication ignores orchestrator component", func(t *testing.T) {
		t.Parallel()
		cr := &psv1.PerconaServerMySQL{}
		inst := instanceWithTopology(common.TopologyGroupReplication, map[string]corev1alpha1.ComponentSpec{
			common.ComponentOrchestrator: {
				Replicas: ptr32(3),
				Image:    "example/orchestrator:dev",
			},
		})
		if err := applyOrchestrator(cr, inst, spec); err != nil {
			t.Fatal(err)
		}
		if cr.Spec.Orchestrator.Enabled {
			t.Fatal("orchestrator must be off on group-replication")
		}
		if cr.Spec.Unsafe.Orchestrator {
			t.Fatal("unsafe flag is not used on group-replication")
		}
		if cr.Spec.Orchestrator.Image != "" || cr.Spec.Orchestrator.Size != 0 {
			t.Fatal("orchestrator spec must not be mapped on group-replication")
		}
	})

}

func instanceWithTopology(topology string, components map[string]corev1alpha1.ComponentSpec) *corev1alpha1.Instance {
	inst := &corev1alpha1.Instance{}
	if topology != "" {
		inst.Spec.Topology = &corev1alpha1.TopologySpec{Type: topology}
	}
	inst.Spec.Components = components
	return inst
}

func ptr32(v int32) *int32 {
	return &v
}
