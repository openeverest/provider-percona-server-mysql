// Package ps contains the schema-bearing Go types for the
// "ps" BackupClass. Each struct here is converted to an OpenAPI
// v3 schema by `provider-sdk generate` and inlined into the generated
// BackupClass manifest.
//
// +k8s:openapi-gen=true
package ps

// PsBackupParameters describes the parameters accepted by Backup CRs that
// target this class (spec.parameters). Add fields the user can set per backup.
type PsBackupParameters struct{}

// PsRestoreParameters describes the parameters accepted by Restore CRs that
// target this class (spec.parameters). Add fields the user can set per restore.
type PsRestoreParameters struct{}

// PsPITRParameters describes the per-storage PITR parameters exposed to
// Instance.spec.backup.storages[].pitr.parameters. Add fields a provider needs
// to fine-tune its PITR pipeline (oplog span, compression, retention, etc.).
type PsPITRParameters struct{}
