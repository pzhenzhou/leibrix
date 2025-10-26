# Leibrix

A distributed, memory-centric acceleration layer for interactive analytics and operational reporting.

## Background

Modern OLAP systems struggle with performance and scalability challenges as business units increasingly rely on self-service analytics and real-time dashboards. Key issues include:

- **Latency Unpredictability**: Complex SQL queries cause high tail latencies (P95/P99), undermining user trust
- **Inefficient Multi-Tenancy**: Resource isolation is difficult on shared clusters, leading to "noisy neighbor" problems
- **High Operational Costs**: Deploying separate clusters per tenant is not scalable or cost-effective

Leibrix addresses these challenges by providing a distributed memory layer that sits above existing lakehouse/OLAP infrastructure, delivering predictable millisecond-level latency with strong multi-tenant isolation.

## Core Features

### Memory-Centric Architecture
- Materializes immutable, epoched datasets in memory for millisecond-level query latency
- Leverages embedded DuckDB for vectorized execution and complex SQL capabilities
- Serves data directly from worker memory with predictable performance

### Distributed & Scalable
- Master-worker architecture with horizontal scaling capability
- Embedded etcd quorum for distributed coordination and fault tolerance
- Stateless gateway layer for unified client access and intelligent routing

### Multi-Tenant Isolation
- Per-tenant memory and CPU budgets with strict admission control
- Per-tenant concurrency caps for workload isolation
- Benefit-driven residency model: admit/evict shards by (saved scan × heat ÷ memory)

### Immutable Epoch Model
- Treats datasets as read-only snapshots tied to time windows
- Simplifies consistency and concurrency management
- State machine: LOADING → VALIDATING → READY → RETIRING

### Intelligent Data Flow
- Notify-pull model: master emits LoadPlans, workers pull and validate
- Epoch token fencing ensures only READY epochs serve traffic
- Bounded query execution with fallback to source OLAP systems

## Architecture

- **Master (Control Plane)**: Manages placement, quotas, epochs, health checks, and worker orchestration
- **Worker (Data Plane)**: In-memory execution engine with embedded DuckDB for vectorized analytics
- **Gateway**: Resolves `{tenant, dataset, predicate}` → `{worker, epoch}` and handles routing/failover

## Limitations

- Not a system of record—the source OLAP/lakehouse remains authoritative
- Optimized for analytical (OLAP) queries, not transactional (OLTP) workloads
- Data freshness tied to upstream pipeline completion intervals

