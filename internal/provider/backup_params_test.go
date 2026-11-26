package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"

	"github.com/openeverest/provider-percona-server-mysql/internal/common"
)

func raw(s string) *runtime.RawExtension {
	return &runtime.RawExtension{Raw: []byte(s)}
}

func TestResolveBackupType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		params   *runtime.RawExtension
		wantType psv1.BackupType
		wantErr  string
	}{
		{name: "absent", wantType: psv1.BackupTypeFull},
		{name: "empty", params: raw(""), wantType: psv1.BackupTypeFull},
		{name: "null", params: raw("null"), wantType: psv1.BackupTypeFull},
		{name: "empty object", params: raw("{}"), wantType: psv1.BackupTypeFull},
		{name: "full", params: raw(`{"type":"full"}`), wantType: psv1.BackupTypeFull},
		{name: "incremental", params: raw(`{"type":"incremental"}`), wantType: psv1.BackupTypeIncremental},
		{name: "unknown type", params: raw(`{"type":"weekly"}`), wantErr: `unsupported backup type "weekly"`},
		{name: "upper-case type", params: raw(`{"type":"FULL"}`), wantErr: `unsupported backup type "FULL"`},
		{name: "type of the wrong JSON type", params: raw(`{"type":5}`), wantErr: "cannot unmarshal number"},
		{name: "undeclared field", params: raw(`{"typ":"full"}`), wantErr: `unknown field "typ"`},
		{name: "not an object", params: raw(`"full"`), wantErr: "invalid parameters"},
		{name: "trailing data", params: raw(`{"type":"full"}{}`), wantErr: "unexpected data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveBackupType(tt.params)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, got)
		})
	}
}

func TestValidateRestoreParameters(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateRestoreParameters(nil))
	require.NoError(t, validateRestoreParameters(raw("{}")))
	require.ErrorContains(t, validateRestoreParameters(raw(`{"target":"x"}`)), `unknown field "target"`)
}

func TestValidateBackupSchedules(t *testing.T) {
	t.Parallel()

	schedules := func(storages ...[]corev1alpha1.InstanceBackupSchedule) *corev1alpha1.InstanceBackupSpec {
		spec := &corev1alpha1.InstanceBackupSpec{Enabled: true, ClassRef: apicommon.ObjectRef{Name: "ps"}}
		for i, list := range storages {
			spec.Storages = append(spec.Storages, corev1alpha1.InstanceBackupStorage{
				StorageRef: apicommon.ObjectRef{Name: "minio"},
				Schedules:  list,
			})
			_ = i
		}
		return spec
	}

	t.Run("nil backup spec is valid", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, validateBackupSchedules(nil))
	})

	t.Run("distinct names across storages are valid", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(schedules(
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly-full", Cron: "0 1 * * *"}},
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly-incr", Cron: "0 2 * * *", Parameters: raw(`{"type":"incremental"}`)}},
		))
		assert.Empty(t, errs)
	})

	t.Run("two schedules of the same type on a storage are valid", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(schedules([]corev1alpha1.InstanceBackupSchedule{
			{Name: "weekly-full", Cron: "0 1 * * 0"},
			{Name: "monthly-full", Cron: "0 1 1 * *"},
		}))
		assert.Empty(t, errs, "the PS operator's Schedule field is a list; no one-per-type limit applies")
	})

	t.Run("duplicate schedule name across storages is rejected", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(schedules(
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 1 * * *"}},
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 2 * * *"}},
		))
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[1].schedules[0].name", errs[0].Field)
	})

	t.Run("schedule parameters with an unknown type is rejected", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(schedules([]corev1alpha1.InstanceBackupSchedule{
			{Name: "nightly", Cron: "0 1 * * *", Parameters: raw(`{"type":"weekly"}`)},
		}))
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[0].schedules[0].parameters", errs[0].Field)
	})

	t.Run("schedule parameters with an undeclared field is rejected", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(schedules([]corev1alpha1.InstanceBackupSchedule{
			{Name: "nightly", Cron: "0 1 * * *", Parameters: raw(`{"compress":true}`)},
		}))
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[0].schedules[0].parameters", errs[0].Field)
	})
}

func TestSyncRestoreRejectsInvalidParameters(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, psv1.AddToScheme(scheme))

	instance := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "ps-prod", Namespace: "everest"}}
	restore := &backupv1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "everest"},
		Spec: backupv1alpha1.RestoreSpec{
			InstanceRef: apicommon.ObjectRef{Name: "ps-prod"},
			DataSource: backupv1alpha1.DataSource{
				Type:   backupv1alpha1.DataSourceTypeBackup,
				Backup: &backupv1alpha1.DataSourceBackup{BackupRef: apicommon.ObjectRef{Name: "b1"}},
			},
			Parameters: raw(`{"target":"x"}`),
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, restore.DeepCopy()).Build()
	c := controller.NewContext(context.Background(), k8sClient, instance, common.ProviderName)

	status, err := (&Provider{}).SyncRestore(c, restore)

	require.NoError(t, err)
	assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
	assert.Contains(t, status.Message, `spec.parameters: invalid parameters: json: unknown field "target"`)
}
