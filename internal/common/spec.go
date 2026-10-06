// Package common defines shared constants used across the provider.
package common

const (
	// ProviderName is this provider's identity: the name of the Provider CR it
	// ships and the value Instances put in spec.providerRef.name. It must match
	// `name` in definition/provider.yaml — the runtime uses it to fetch the
	// Provider CR, so a mismatch means nothing ever reconciles.
	ProviderName = "percona-server-mysql"

	ComponentEngine           = "engine"
	ComponentTypeMysql        = "mysql"
	ComponentOrchestrator     = "orchestrator"
	ComponentTypeOrchestrator = "orchestrator"
	ComponentTypeToolkit      = "toolkit"
	ComponentProxy            = "proxy"
	ProxyTypeHAProxy          = "haproxy"
	ProxyTypeRouter           = "router"

	// ComponentBackup is the component type key used in the version catalog
	// (definition/versions.yaml) for the xtrabackup image used to run
	// backups and restores.
	ComponentBackup = "backup"

	ComponentMonitoring = "monitoring"
	MonitoringTypePMM   = "pmm"

	TopologyAsync            = "async"
	TopologyGroupReplication = "groupreplication"
)
