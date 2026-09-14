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

// OrchestratorCustomSpec defines custom configuration for orchestrator components.
// Add fields here when the orchestrator component type needs custom configuration
// beyond what the base Instance spec provides.
type OrchestratorCustomSpec struct{}
