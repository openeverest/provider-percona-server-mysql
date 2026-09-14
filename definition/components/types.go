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
