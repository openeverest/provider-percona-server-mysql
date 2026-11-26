package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"

	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
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

// schedules gives the Instance one backup storage with the given schedules,
// all enabled.
func schedules(list ...corev1alpha1.InstanceBackupSchedule) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		for i := range list {
			list[i].Enabled = true
		}
		in.Spec.Backup = &corev1alpha1.InstanceBackupSpec{
			Enabled:  true,
			ClassRef: apicommon.ObjectRef{Name: "ps"},
			Storages: []corev1alpha1.InstanceBackupStorage{{
				StorageRef: apicommon.ObjectRef{Name: "minio"},
				Schedules:  list,
			}},
		}
	}
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
		{
			name:    "schedule name is an arbitrary user-facing identifier",
			fixture: asyncFixture,
			mutate:  schedules(corev1alpha1.InstanceBackupSchedule{Name: "nightly-full", Cron: "0 1 * * *"}),
		},
		{
			name:    "two schedules of the same inferred type on a storage are allowed",
			fixture: asyncFixture,
			mutate: schedules(
				corev1alpha1.InstanceBackupSchedule{Name: "weekly-full", Cron: "0 1 * * 0"},
				corev1alpha1.InstanceBackupSchedule{Name: "monthly-full", Cron: "0 1 1 * *"},
			),
		},
		{
			name:    "duplicate schedule name",
			fixture: asyncFixture,
			mutate: schedules(
				corev1alpha1.InstanceBackupSchedule{Name: "nightly", Cron: "0 1 * * *"},
				corev1alpha1.InstanceBackupSchedule{Name: "nightly", Cron: "0 2 * * *", Parameters: raw(`{"type":"incremental"}`)},
			),
			want: []string{"spec.backup.storages[0].schedules[1].name"},
		},
		// Whether a schedule's Parameters conform to the resolved
		// BackupClass's parametersSchema is checked by
		// validateBackupScheduleParameters, which needs a live BackupClass
		// read and so runs from ValidateReferences — see
		// TestValidateBackupScheduleParameters in backup_params_test.go, not
		// this admission-only (Context-less) harness.
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
