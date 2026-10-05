// Package async contains custom spec types for the async topology.
//
// Add fields to AsyncTopologyConfig and reference it via configSchema in
// topology.yaml when this topology needs custom configuration.
//
// +k8s:openapi-gen=true
package async

// AsyncTopologyConfig defines configuration for the async topology.
// Add fields here when the async topology needs custom configuration
// beyond what the base Instance spec provides.
//
// Example:
//
//	type AsyncTopologyConfig struct {
//	    NumShards int32 `json:"numShards,omitempty"`
//	}
//
// Then reference it in topology.yaml:
//
//	config:
//	  configSchema: AsyncTopologyConfig
type AsyncTopologyConfig struct{}
