package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"

	"github.com/openeverest/provider-percona-server-mysql/definition/backupclasses/ps"
)

// Neither the core webhooks nor the runtime check Backup.spec.parameters,
// Restore.spec.parameters or InstanceBackupSchedule.parameters against the
// ps BackupClass's schemas, so the provider does.

// parseBackupParameters decodes backup parameters (Backup.spec.parameters or
// InstanceBackupSchedule.parameters) as the ps class's parametersSchema
// declares them: no fields beyond PsBackupParameters's.
func parseBackupParameters(raw *runtime.RawExtension) (ps.PsBackupParameters, error) {
	var cfg ps.PsBackupParameters
	if err := decodeStrict(raw, &cfg); err != nil {
		return ps.PsBackupParameters{}, err
	}
	return cfg, nil
}

// validateRestoreParameters checks restore parameters against the ps class's
// restoreParametersSchema, which declares no fields.
func validateRestoreParameters(raw *runtime.RawExtension) error {
	return decodeStrict(raw, &ps.PsRestoreParameters{})
}

// decodeStrict decodes a parameters payload into target, rejecting fields
// target does not declare. An absent or null payload leaves target as is.
func decodeStrict(raw *runtime.RawExtension, target any) error {
	if raw == nil || len(bytes.TrimSpace(raw.Raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw.Raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("invalid parameters: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("invalid parameters: unexpected data after the JSON object")
	}
	return nil
}

// validateBackupSchedules checks the schedules of each backup storage: each
// schedule's Name must be unique across the whole Instance (the PS operator
// uses it as the BackupSchedule entry's name), and its Parameters, when set,
// must decode cleanly against the ps class's schema and name a known backup
// type (resolveBackupType). Unlike some operators, the PS operator's
// Schedule field is a list, so multiple schedules resolving to the same
// backup type are allowed.
func validateBackupSchedules(backup *corev1alpha1.InstanceBackupSpec) field.ErrorList {
	if backup == nil {
		return nil
	}
	var errs field.ErrorList
	storagesPath := field.NewPath("spec", "backup", "storages")
	seenNames := map[string]bool{}
	for i, storage := range backup.Storages {
		for j, schedule := range storage.Schedules {
			path := storagesPath.Index(i).Child("schedules").Index(j)

			if seenNames[schedule.Name] {
				errs = append(errs, field.Duplicate(path.Child("name"), schedule.Name))
			}
			seenNames[schedule.Name] = true

			if _, err := resolveBackupType(schedule.Parameters); err != nil {
				errs = append(errs, field.Invalid(path.Child("parameters"), field.OmitValueType{}, err.Error()))
			}
		}
	}
	return errs
}
