# Leibrix

**Distributed coordination and control plane for the Leibrix memory-centric acceleration layer.**

## What is Leibrix?

Leibrix is the Master component of a distributed in-memory acceleration system for interactive analytics. It serves as
the **stateful control plane** that orchestrates data placement, lifecycle management, and cluster coordination across a
pool of Worker nodes.

## Role & Responsibilities

- **Cluster Coordination**: Embedded etcd quorum for distributed consensus and leader election
- **Worker Management**: Health monitoring, registration, and failure handling
- **Admission Control**: Validates dataset requests, generates LoadPlans, enforces quotas
- **Data Assignment**: Determines Worker placement for data epochs using configurable strategies
- **Epoch Lifecycle**: Manages dataset versions from admission through retirement
- **Multi-Tenant Governance**: Per-tenant resource budgets, admission policies, and isolation

## Core Features

### Distributed Coordination

- Leader election via embedded etcd for high availability
- Automatic failover with minimal downtime
- Consistent state management across Master cluster

### Intelligent Admission Control

- Source-agnostic LoadPlan generation (Iceberg, StarRocks, JDBC)
- Per-tenant memory and CPU quota enforcement
- Benefit-driven admission: prioritize hot data by (scan_cost × heat ÷ memory)

### Worker Orchestration

- Persistent gRPC bidirectional streams for real-time coordination
- Heartbeat-based liveness detection with automatic rebalancing
- Graceful drain and shutdown coordination

### Pluggable Assignment Strategies

- Round-robin for homogeneous clusters
- Hash-based for consistent placement
- Capacity-aware for heterogeneous environments

## Architecture

Leibrix is a Go-based service that communicates via gRPC:

- **External API** (`ManagementService`): Unary RPCs for dataset admission and management
- **Internal API** (`ControlPlaneService`): Event streams for Worker coordination
- **State Store**: Embedded etcd for cluster state and configuration

## Related Components

- **[leibrix-worker](https://github.com/pzhenzhou/leibrix-worker)**: Memory-centric data plane with embedded DuckDB
- **leibrix-gateway** (future): Unified query routing layer with MySQL protocol support

## Limitations

- Not a data storage system—delegates actual data serving to Workers
- Requires at least 3 nodes for production HA (etcd quorum)
- State migration during version upgrades requires careful planning
