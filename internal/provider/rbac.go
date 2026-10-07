package provider

// Run `make manifests` to regenerate config/rbac/role.yaml from these markers.
// This file contains kubebuilder RBAC markers for controller-gen.
// See: https://book.kubebuilder.io/reference/markers/rbac

// Base RBAC (required by all providers):
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=core.openeverest.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// The runtime writes the connection details returned by Status() into a Secret.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

// =============================================================================
// PROVIDER-SPECIFIC RBAC
// =============================================================================

// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqls,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqls/status,verbs=get
// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqls/finalizers,verbs=update
// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlbackups/status,verbs=get
// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlbackups/finalizers,verbs=update
// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlrestores/status,verbs=get
// +kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlrestores/finalizers,verbs=update
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backupclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backupstorages,verbs=get;list;watch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backups/finalizers,verbs=update
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=restores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=restores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=restores/finalizers,verbs=update
// +kubebuilder:rbac:groups=monitoring.openeverest.io,resources=monitoringconfigs,verbs=get;list;watch
