// Package groupreplication contains custom spec types for the group-replication topology.
//
// Add fields to GroupReplicationTopologyConfig and reference it via configSchema in
// topology.yaml when this topology needs custom configuration.
//
// +k8s:openapi-gen=true
package groupreplication

// GroupReplicationTopologyConfig defines configuration for the group-replication topology.
// Add fields here when the group-replication topology needs custom configuration
// beyond what the base Instance spec provides.
//
// Example:
//
//	type GroupReplicationTopologyConfig struct {
//	    NumShards int32 `json:"numShards,omitempty"`
//	}
//
// Then reference it in topology.yaml:
//
//	config:
//	  configSchema: GroupReplicationTopologyConfig
type GroupReplicationTopologyConfig struct{}
