package provider

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

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

// generatedPSBackupClassPath is the BackupClass manifest `provider-sdk
// generate` derives from definition/backupclasses/ps, including the
// parametersSchema/restoreParametersSchema the kubebuilder markers on
// ps.PsBackupParameters / PsRestoreParameters produce. Tests load the real
// generated artifact so they exercise exactly what the provider ships.
const generatedPSBackupClassPath = "../../charts/provider-percona-server-mysql/generated/backupclasses/ps.yaml"

func loadPSBackupClass(t *testing.T) *backupv1alpha1.BackupClass {
	t.Helper()
	data, err := os.ReadFile(generatedPSBackupClassPath)
	require.NoError(t, err)
	bc := &backupv1alpha1.BackupClass{}
	require.NoError(t, yaml.Unmarshal(data, bc))
	return bc
}

func TestResolveBackupType(t *testing.T) {
	t.Parallel()
	bc := loadPSBackupClass(t)

	tests := []struct {
		name       string
		params     *runtime.RawExtension
		wantType   psv1.BackupType
		wantErr    string
		wantSchema bool
	}{
		{name: "absent", wantType: psv1.BackupTypeFull},
		{name: "empty", params: raw(""), wantType: psv1.BackupTypeFull},
		{name: "null", params: raw("null"), wantType: psv1.BackupTypeFull},
		{name: "empty object", params: raw("{}"), wantType: psv1.BackupTypeFull},
		{name: "full", params: raw(`{"type":"full"}`), wantType: psv1.BackupTypeFull},
		{name: "incremental", params: raw(`{"type":"incremental"}`), wantType: psv1.BackupTypeIncremental},
		{name: "unknown type", params: raw(`{"type":"weekly"}`), wantErr: `must be one of the following: "full", "incremental"`, wantSchema: true},
		{name: "upper-case type", params: raw(`{"type":"FULL"}`), wantErr: `must be one of the following: "full", "incremental"`, wantSchema: true},
		{name: "type of the wrong JSON type", params: raw(`{"type":5}`), wantErr: "Invalid type", wantSchema: true},
		{name: "undeclared field", params: raw(`{"typ":"full"}`), wantErr: "Additional property typ is not allowed", wantSchema: true},
		{name: "not an object", params: raw(`"full"`), wantErr: "failed to unmarshal parameters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveBackupType(bc, tt.params)
			if tt.wantErr != "" {
				if tt.wantSchema {
					require.ErrorIs(t, err, apicommon.ErrSchemaValidationFailure)
				}
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, got)
		})
	}

	t.Run("no resolved class rejects any payload", func(t *testing.T) {
		t.Parallel()
		_, err := resolveBackupType(nil, raw(`{"type":"full"}`))
		require.ErrorIs(t, err, apicommon.ErrSchemaValidationFailure)
	})
}

func TestValidateRestoreParameters(t *testing.T) {
	t.Parallel()
	bc := loadPSBackupClass(t)

	require.NoError(t, validateRestoreParameters(bc, nil))
	require.NoError(t, validateRestoreParameters(bc, raw("{}")))
	err := validateRestoreParameters(bc, raw(`{"target":"x"}`))
	require.ErrorIs(t, err, apicommon.ErrSchemaValidationFailure)
	require.ErrorContains(t, err, "Additional property target is not allowed")
}

func TestValidateBackupScheduleNames(t *testing.T) {
	t.Parallel()

	schedules := func(storages ...[]corev1alpha1.InstanceBackupSchedule) *corev1alpha1.InstanceBackupSpec {
		spec := &corev1alpha1.InstanceBackupSpec{Enabled: true, ClassRef: apicommon.ObjectRef{Name: "ps"}}
		for _, list := range storages {
			spec.Storages = append(spec.Storages, corev1alpha1.InstanceBackupStorage{
				StorageRef: apicommon.ObjectRef{Name: "minio"},
				Schedules:  list,
			})
		}
		return spec
	}

	t.Run("nil backup spec is valid", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, validateBackupScheduleNames(nil))
	})

	t.Run("distinct names across storages are valid", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupScheduleNames(schedules(
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly-full", Cron: "0 1 * * *"}},
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly-incr", Cron: "0 2 * * *", Parameters: raw(`{"type":"incremental"}`)}},
		))
		assert.Empty(t, errs)
	})

	t.Run("two schedules on the same storage are valid", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupScheduleNames(schedules([]corev1alpha1.InstanceBackupSchedule{
			{Name: "weekly-full", Cron: "0 1 * * 0"},
			{Name: "monthly-full", Cron: "0 1 1 * *"},
		}))
		assert.Empty(t, errs, "the PS operator's Schedule field is a list; no one-per-type limit applies")
	})

	t.Run("duplicate schedule name across storages is rejected", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupScheduleNames(schedules(
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 1 * * *"}},
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 2 * * *"}},
		))
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[1].schedules[0].name", errs[0].Field)
	})
}

// testContextWithPSBackupClass builds a Context over instance with the real
// generated "ps" BackupClass registered on the fake client, so
// c.BackupClassForInstance() resolves it the way the runtime does.
func testContextWithPSBackupClass(t *testing.T, instance *corev1alpha1.Instance, extra ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, psv1.AddToScheme(scheme))

	bc := loadPSBackupClass(t)
	objs := append([]client.Object{instance, bc}, extra...)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), k8sClient, instance, common.ProviderName)
}

func TestValidateBackupScheduleParameters(t *testing.T) {
	t.Parallel()

	schedules := func(list ...corev1alpha1.InstanceBackupSchedule) *corev1alpha1.Instance {
		return &corev1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{Name: "ps-prod", Namespace: "everest"},
			Spec: corev1alpha1.InstanceSpec{
				Backup: &corev1alpha1.InstanceBackupSpec{
					Enabled:  true,
					ClassRef: apicommon.ObjectRef{Name: "ps"},
					Storages: []corev1alpha1.InstanceBackupStorage{{
						StorageRef: apicommon.ObjectRef{Name: "minio"},
						Schedules:  list,
					}},
				},
			},
		}
	}

	t.Run("no backup config is valid", func(t *testing.T) {
		t.Parallel()
		c := testContextWithPSBackupClass(t, &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "ps-prod", Namespace: "everest"}})
		errs, err := validateBackupScheduleParameters(c)
		require.NoError(t, err)
		assert.Empty(t, errs)
	})

	t.Run("known type is valid", func(t *testing.T) {
		t.Parallel()
		c := testContextWithPSBackupClass(t, schedules(corev1alpha1.InstanceBackupSchedule{
			Name: "nightly", Cron: "0 1 * * *", Parameters: raw(`{"type":"incremental"}`),
		}))
		errs, err := validateBackupScheduleParameters(c)
		require.NoError(t, err)
		assert.Empty(t, errs)
	})

	t.Run("unknown type is rejected", func(t *testing.T) {
		t.Parallel()
		c := testContextWithPSBackupClass(t, schedules(corev1alpha1.InstanceBackupSchedule{
			Name: "nightly", Cron: "0 1 * * *", Parameters: raw(`{"type":"weekly"}`),
		}))
		errs, err := validateBackupScheduleParameters(c)
		require.NoError(t, err)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[0].schedules[0].parameters", errs[0].Field)
	})

	t.Run("undeclared field is rejected", func(t *testing.T) {
		t.Parallel()
		c := testContextWithPSBackupClass(t, schedules(corev1alpha1.InstanceBackupSchedule{
			Name: "nightly", Cron: "0 1 * * *", Parameters: raw(`{"compress":true}`),
		}))
		errs, err := validateBackupScheduleParameters(c)
		require.NoError(t, err)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[0].schedules[0].parameters", errs[0].Field)
	})
}

func TestSyncRestoreRejectsInvalidParameters(t *testing.T) {
	t.Parallel()

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "ps-prod", Namespace: "everest"},
		Spec:       corev1alpha1.InstanceSpec{Backup: &corev1alpha1.InstanceBackupSpec{ClassRef: apicommon.ObjectRef{Name: "ps"}}},
	}
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
	c := testContextWithPSBackupClass(t, instance, restore.DeepCopy())

	status, err := (&Provider{}).SyncRestore(c, restore)

	require.NoError(t, err)
	assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
	assert.Contains(t, status.Message, "spec.parameters: schema validation failed")
	assert.Contains(t, status.Message, "Additional property target is not allowed")
}

func TestSyncBackupRejectsInvalidParameters(t *testing.T) {
	t.Parallel()

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "ps-prod", Namespace: "everest"},
		Spec:       corev1alpha1.InstanceSpec{Backup: &corev1alpha1.InstanceBackupSpec{ClassRef: apicommon.ObjectRef{Name: "ps"}}},
	}
	backup := &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: "everest"},
		Spec: backupv1alpha1.BackupSpec{
			Origin:     backupv1alpha1.BackupOrigin{Type: backupv1alpha1.BackupOriginTypeInstance, InstanceRef: &apicommon.ObjectRef{Name: "ps-prod"}},
			ClassRef:   apicommon.ObjectRef{Name: "ps"},
			StorageRef: apicommon.ObjectRef{Name: "minio"},
			Parameters: raw(`{"type":"weekly"}`),
		},
	}
	c := testContextWithPSBackupClass(t, instance, backup.DeepCopy())

	status, err := (&Provider{}).SyncBackup(c, backup)

	require.NoError(t, err)
	assert.Equal(t, backupv1alpha1.BackupStateFailed, status.State)
	assert.Contains(t, status.Message, "spec.parameters: schema validation failed")
}
