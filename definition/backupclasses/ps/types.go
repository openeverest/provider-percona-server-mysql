// Package ps contains the schema-bearing Go types for the
// "ps" BackupClass. Each struct here is converted to an OpenAPI
// v3 schema by `provider-sdk generate` and inlined into the generated
// BackupClass manifest.
//
// +k8s:openapi-gen=true
package ps

// PsBackupType selects the xtrabackup backup type. Matches
// PerconaServerMySQLBackupSpec.Type ("full" or "incremental") on the
// operator's PerconaServerMySQLBackup CR.
// +kubebuilder:validation:Enum=full;incremental
type PsBackupType string

// PsBackupParameters describes the parameters accepted by Backup CRs that
// target this class (spec.parameters). Add fields the user can set per backup.
type PsBackupParameters struct {
	// Type selects the xtrabackup backup type. "full" (default) creates a
	// self-contained backup. "incremental" backs up only the data changed
	// since the base (full) backup — the operator picks the latest
	// Succeeded full backup on the same storage as the base when
	// incrementalBaseBackupName is not set.
	// +kubebuilder:default=full
	// +kubebuilder:validation:Enum=full;incremental
	// +optional
	Type PsBackupType `json:"type,omitempty"`
}

// PsRestoreParameters describes the parameters accepted by Restore CRs that
// target this class (spec.parameters). Add fields the user can set per restore.
type PsRestoreParameters struct{}

// PsPITRParameters describes the per-storage PITR parameters exposed to
// Instance.spec.backup.storages[].pitr.parameters. Add fields a provider needs
// to fine-tune its PITR pipeline (oplog span, compression, retention, etc.).
type PsPITRParameters struct{}
