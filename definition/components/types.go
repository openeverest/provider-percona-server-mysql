// Package components contains parameter types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type accepts parameters beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

// MysqlCustomSpec defines custom configuration for mysql components.
// Add fields here when the mysql component type needs custom configuration
// beyond what the base Instance spec provides.
type MysqlCustomSpec struct{}

// OrchestratorCustomSpec is intentionally empty. Enable/disable is the
// presence of spec.components.orchestrator; image, replicas, resources,
// affinity, and service are standard ComponentSpec fields mapped in Sync.
type OrchestratorCustomSpec struct{}

// HaproxyCustomSpec is intentionally empty. Proxy size, image, resources,
// affinity, and service are standard ComponentSpec fields.
type HaproxyCustomSpec struct{}

// RouterCustomSpec is intentionally empty. Proxy size, image, resources,
// affinity, and service are standard ComponentSpec fields.
type RouterCustomSpec struct{}

// PMMParameters defines structured parameters for PMM monitoring.
type PMMParameters struct {
	// MonitoringConfigName specifies the name of the MonitoringConfig resource
	// to use for configuring PMM monitoring.
	// If not specified, monitoring will not be configured.
	MonitoringConfigName *string `json:"monitoringConfigName,omitempty"`
}

// MysqlParameters defines custom parameters for mysql components.
type MysqlParameters struct {
	// Configuration allows specifying custom MySQL configuration
	// parameters as a YAML document of flat key/value pairs, e.g.:
	//
	//   max_connections: "500"
	//   innodb_buffer_pool_size: 1G
	//   innodb_flush_log_at_trx_commit: 2
	//
	// applied on top of, and overrides, the operator's built-in defaults.
	// +optional
	Configuration string `json:"configuration,omitempty"`
}
