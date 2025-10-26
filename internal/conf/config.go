package conf

import (
	"errors"

	goyaml "github.com/goccy/go-yaml"

	"os"
)

// LeibrixConfig is the top-level configuration entry point
type LeibrixConfig struct {
	Node          NodeConfig    `yaml:"node"`
	ClusterConfig ClusterConfig `yaml:"cluster"`

	// FencingToken is an optional bootstrap/recovery field for manually setting
	// the minimum epoch number. This is typically NOT set in normal operations.
	//
	// Use cases:
	//   1. Disaster recovery: When restoring from backup, you can set this to ensure
	//      the recovered cluster's epoch is higher than any previously issued epoch,
	//      preventing old commands from being accepted.
	//   2. Cluster migration: When migrating to a new cluster, set this higher than
	//      the old cluster's last epoch to prevent cross-cluster command confusion.
	//
	// In normal operation, this should be 0 (unset), and etcd's CreateRevision
	// will naturally provide monotonic fencing tokens.
	FencingToken uint64 `yaml:"fencing_token,omitempty"`
}

// NodeConfig defines the identity and local settings for this node
type NodeConfig struct {
	NodeName      string `yaml:"node_name"`
	HostName      string `yaml:"host_name" default:"localhost"`
	DataDir       string `yaml:"data_dir" default:"/tmp/leibrix/master"`
	RPCPort       int    `yaml:"rpc_port" default:"7003"`
	ListenPort    int    `yaml:"listen_port" default:"2380"`
	AdvertisePort int    `yaml:"advertise_addr" default:"2382"`
}

// ClusterConfig contains all embedded etcd cluster configuration
type ClusterConfig struct {
	// Embedded etcd server endpoint configuration
	// ListenClientUrls: Where THIS node's embedded etcd server listens for client connections.
	//                   The LeibrixLeaderElection on this node connects to these URLs.
	ListenClientUrls []string `yaml:"listen_client_urls"`

	// AdvertisePeerUrls: Where THIS node's embedded etcd server listens for peer connections.
	//                    Other etcd nodes in the cluster connect to these URLs for Raft consensus.
	AdvertisePeerUrls []string `yaml:"advertise_peer_urls"`

	// Cluster topology (immutable after first bootstrap)
	// Example: "node1=http://10.0.1.1:2380,node2=http://10.0.1.2:2380,node3=http://10.0.1.3:2380"
	InitialCluster      string `yaml:"initial_cluster"`
	InitialClusterToken string `yaml:"initial_cluster_token"`

	// Performance tuning (optional, with sensible defaults)
	SnapshotCount uint64 `yaml:"snapshot_count,omitempty"`
	HeartbeatMs   uint   `yaml:"heartbeat_ms,omitempty"`
	ElectionMs    uint   `yaml:"election_ms,omitempty"`
	LogLevel      string `yaml:"log_level,omitempty"`
}

func LoadConfig(path string) (*LeibrixConfig, error) {
	if path == "" {
		return nil, errors.New("config file path is empty")
	}
	yamlFile, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	decoder := goyaml.NewDecoder(yamlFile)
	var config LeibrixConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}
