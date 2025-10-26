# Leibrix Configuration Files

This directory contains sample configuration files for Leibrix, a distributed in-memory acceleration layer built on embedded etcd.

## Configuration Files

### Single Node (Development)
- **`leibrix-single-node.yaml`**: Configuration for running a single node on localhost
  - Suitable for development and testing
  - No cluster dependencies
  - Quick start setup

### Multi-Node Cluster (Production)
- **`leibrix-cluster-node1.yaml`**: Configuration for node 1 of a 3-node cluster
- **`leibrix-cluster-node2.yaml`**: Configuration for node 2 of a 3-node cluster  
- **`leibrix-cluster-node3.yaml`**: Configuration for node 3 of a 3-node cluster

These configurations provide high availability through Raft consensus. A 3-node cluster can tolerate 1 node failure while maintaining quorum.

## Configuration Structure

### Node Configuration
```yaml
node:
  node_name: "leibrix-node1"      # Unique node identifier
  host_name: "10.0.1.101"          # IP/hostname for this node
  data_dir: "/var/lib/leibrix"     # etcd data storage path
  rpc_port: 7003                   # Leibrix RPC service port
  listen_port: 2380                # Internal communication port
  advertise_addr: 2382             # Advertised port to peers
```

### Cluster Configuration
```yaml
cluster:
  # Client connection URLs (for application clients)
  listen_client_urls:
    - "http://10.0.1.101:2379"
  
  # Peer URLs (for Raft consensus between etcd nodes)
  advertise_peer_urls:
    - "http://10.0.1.101:2380"
  
  # All nodes in the cluster (must be identical across all nodes)
  initial_cluster: "node1=http://10.0.1.101:2380,node2=http://10.0.1.102:2380,node3=http://10.0.1.103:2380"
  
  # Cluster identification token (must be identical across all nodes)
  initial_cluster_token: "leibrix-cluster-prod-001"
  
  # Performance tuning
  snapshot_count: 10000       # Transactions before snapshot
  heartbeat_ms: 100           # Heartbeat interval (default: 100ms)
  election_ms: 1000           # Election timeout (must be >= 10 * heartbeat_ms)
  log_level: "info"           # debug, info, warn, error
```

### Fencing Token (Optional)
```yaml
fencing_token: 0  # Only set for disaster recovery scenarios
```

The fencing token provides protection against split-brain scenarios during:
1. **Disaster Recovery**: Set higher than the last known epoch when restoring from backup
2. **Cluster Migration**: Set higher than old cluster's epoch to prevent cross-cluster conflicts

In normal operations, leave this at 0 (or omit it).

## Quick Start

### Development (Single Node)
```bash
# Copy the example configuration
cp examples/config/leibrix-single-node.yaml config.yaml

# Edit for your environment (optional for development)
vim config.yaml

# Start the server (uses ./config.yaml by default)
./leibrix-srv

# Or specify the config path directly
LEIBRIX_CONFIG_PATH=examples/config/leibrix-single-node.yaml ./leibrix-srv
```

### Production (3-Node Cluster)

**Important**: When bootstrapping a new cluster:
1. All nodes must use the **same** `initial_cluster` and `initial_cluster_token`
2. Start all nodes within a short time window (recommended: < 30 seconds)
3. Each node should have its own unique `node_name`, `host_name`, `data_dir`, and URLs

```bash
# On node 1 (10.0.1.101)
# Copy and customize the configuration
cp examples/config/leibrix-cluster-node1.yaml config.yaml
vim config.yaml  # Update IPs, paths, etc.
./leibrix-srv

# On node 2 (10.0.1.102)
cp examples/config/leibrix-cluster-node2.yaml config.yaml
vim config.yaml
./leibrix-srv

# On node 3 (10.0.1.103)
cp examples/config/leibrix-cluster-node3.yaml config.yaml
vim config.yaml
./leibrix-srv
```

## Production Deployment Checklist

- [ ] Replace IP addresses in cluster configs with actual node IPs
- [ ] Use a unique `initial_cluster_token` for each environment (dev/staging/prod)
- [ ] Ensure `data_dir` has sufficient disk space and proper permissions
- [ ] Configure firewalls to allow:
  - Port 2379 (client connections)
  - Port 2380 (peer communication)
  - Port 7003 (Leibrix RPC)
- [ ] For cross-datacenter deployments, increase `election_ms` to 3000-5000ms
- [ ] Set up monitoring for etcd metrics
- [ ] Configure regular backups of etcd data directories
- [ ] Use TLS for production (update URLs from http:// to https://)

## Network Latency Tuning

For clusters with higher network latency (e.g., cross-datacenter):

```yaml
cluster:
  heartbeat_ms: 200       # Increase heartbeat interval
  election_ms: 3000       # Increase election timeout (>= 10 * heartbeat_ms)
```

Higher values trade off failure detection speed for cluster stability.

## Troubleshooting

### Cluster Won't Form
- Verify all nodes have identical `initial_cluster` and `initial_cluster_token`
- Check network connectivity between all peer URLs
- Ensure data directories are empty on first bootstrap
- Check firewall rules allow peer communication

### Split Brain Recovery
If you encounter split-brain scenarios after network partitions:
1. Stop all nodes
2. Identify the node with the highest committed index
3. Remove `data_dir/member` on other nodes
4. Set `fencing_token` higher than any previous epoch
5. Restart the cluster

### Performance Issues
- Increase `snapshot_count` if you have high write throughput
- Monitor etcd metrics: leader election count, fsync duration, etc.
- Ensure disk I/O is fast (SSDs recommended)

## Security Recommendations

For production deployments:
1. **Enable TLS**: Use HTTPS URLs and configure client/peer certificates
2. **Authentication**: Enable etcd authentication and create separate users
3. **Network Isolation**: Deploy nodes in a private network
4. **Firewall Rules**: Restrict access to only necessary ports and IPs
5. **Data Encryption**: Enable etcd data-at-rest encryption

## References

- [etcd Configuration Documentation](https://etcd.io/docs/latest/op-guide/configuration/)
- [etcd Clustering Guide](https://etcd.io/docs/latest/op-guide/clustering/)
- [Raft Consensus Algorithm](https://raft.github.io/)

