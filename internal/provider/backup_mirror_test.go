package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestBackupTypeParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		backupType psv1.BackupType
		wantNil    bool
		want       string
	}{
		{name: "empty type yields no parameters", backupType: "", wantNil: true},
		{name: "full", backupType: psv1.BackupTypeFull, want: `{"type":"full"}`},
		{name: "incremental", backupType: psv1.BackupTypeIncremental, want: `{"type":"incremental"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := backupTypeParameters(tt.backupType)
			if tt.wantNil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.JSONEq(t, tt.want, string(got.Raw))

			// Round-trips through the same decode path resolveBackupType uses.
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(got.Raw, &decoded))
			require.Equal(t, string(tt.backupType), decoded["type"])
		})
	}
}

func TestScheduleKeep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		retention *corev1alpha1.BackupScheduleRetention
		want      int
		wantErr   string
	}{
		{name: "unset retention keeps all backups"},
		{
			name: "count retention keeps that many backups",
			retention: &corev1alpha1.BackupScheduleRetention{
				Type:  corev1alpha1.BackupScheduleRetentionTypeCount,
				Count: new(int32(7)),
			},
			want: 7,
		},
		{
			name: "time retention is rejected",
			retention: &corev1alpha1.BackupScheduleRetention{
				Type:     corev1alpha1.BackupScheduleRetentionTypeTime,
				Duration: "30d",
			},
			wantErr: `schedule "daily": time retention is not supported`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := scheduleKeep(corev1alpha1.InstanceBackupSchedule{
				Name:      "daily",
				Enabled:   true,
				Cron:      "0 2 * * *",
				Retention: tt.retention,
			})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
