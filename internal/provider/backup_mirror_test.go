package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
)

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
