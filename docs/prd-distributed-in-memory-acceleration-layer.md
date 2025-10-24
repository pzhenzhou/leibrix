## Project Requirements Document: Distributed In-Memory Acceleration Layer

### Business Problem
Our ability to make timely, data-driven decisions is being hampered by performance and scalability issues within our data infrastructure. As business units increasingly rely on self-service analytics, reporting dashboards, and user insight tools, the underlying OLAP systems are struggling to provide the consistent, low-latency performance required. This leads to several key business problems:

Delayed Insights: Slow-running queries mean that business analysts and decision-makers experience significant delays, slowing down the pace of operations and strategic planning.

Reduced User Adoption: Unreliable performance of BI tools leads to frustration and decreased trust in our data platforms, resulting in lower adoption rates for data-driven practices.

Increased Operational Costs: To guarantee performance and isolation for different business units (tenants), we are forced to deploy and maintain separate, dedicated OLAP clusters. This strategy significantly increases infrastructure and operational overhead.

### Technical Problem Domain

1. Latency unpredictability.
- Complex SQL and variable data volumes cause high tail latencies (P95/P99) on shared OLAP clusters.

- User-facing dashboards and decision workflows suffer from timeouts or jitter, undermining trust.

2. Inefficient Multi-Tenancy: Implementing true multi-tenancy on a single, large OLAP cluster is difficult. Resource management and workload isolation are often insufficient, leading to a "noisy neighbor" problem where a heavy query from one tenant can negatively impact the performance of others. The common workaround—deploying separate clusters per tenant—is not a scalable or cost-effective long-term solution.

### Proposed Solution & Guiding Principles

We will build a distributed, memory-centric acceleration layer that sits above our existing lakehouse/OLAP stack to deliver predictable low latency and strong multi-tenant isolation for interactive analytics and operational reporting. The layer is not a general-purpose database; it materializes immutable, epoched datasets in memory on workers and serves requests through a gateway that routes by dataset/epoch/tenant. A master (embedded etcd quorum) governs placement, quotas, epochs, and failover. Data can originate from table formats (e.g., Iceberg) or OLAP engines —the source is pluggable; the contract is immutable snapshots.

####  Core Principles
- Memory-Centric & Low-Latency: The system's primary goal is to serve data from memory to achieve millisecond-level query latency and predictable performance. It is a speed layer, not a general-purpose database.

- Distributed & Scalable: The architecture will be a master-worker model, allowing it to scale horizontally by adding more worker nodes to increase capacity and throughput.

- Multi-Tenant Support: The system will be designed from the ground up to support multi-tenancy, providing strong resource isolation between different business units or applications.

- Data Immutability: Once data is loaded into a worker's memory for a specific time window (e.g., a day's worth of facts), it is treated as immutable. This simplifies consistency and concurrency management.

#### System Architecture

##### Master / Control Plane
- Acts as the central coordinator or "brain" of the cluster.
- Implemented as a distributed, fault-tolerant service (e.g., using an embedded etcd cluster).
- Responsibilities: Manages worker node status, tracks data placement and partitioning schemes (e.g., which worker holds which date range), and orchestrates data loading.

##### Worker / Data Plane 

- The high-performance data and computation engine.

- Each worker will run an embedded, in-memory analytical engine (DuckDB) to leverage its vectorized execution and complex SQL capabilities.

- Responsibilities: Pulls data from the source system (e.g., StarRocks, Iceberg) upon notification from the master, stores it in memory, and executes queries.

##### Data Gateway

- A unified, stateless access point for all client applications.

- Interface: Will provide an RPC interface initially, with a potential to support a MySQL-compatible SQL interface for seamless BI tool integration.

- Responsibilities: Queries the master to locate the correct worker(s) for a given request, routes the query accordingly, and handles failover.


> Master: embedded etcd quorum. Owns epochs, placement, quotas, admission, assignment, health. Workers: memory-centric executors. Run embedded DuckDB(vectorized, Arrow-native). Implement immutable epoch tables per dataset shard. Gateway: unified access; resolves {tenant, dataset, predicate} → {worker, epoch}; handles retries/failover; supports gRPC/Arrow Flight first; optional MySQL wire later for BI.

#### Approach, Principles, and Constraints

- Data flow (notify-pull):
  -  Upstream publishes a new snapshot/epoch
  - Master emits a LoadPlan (files/segments + schema + predicates + stats).
  - Workers pull, build in-memory immutable tables/pre-aggregates, validate, then mark READY.
  - Gateway routes traffic only to READY epochs (epoch token fencing).

- Principles
  - Immutability & Epochs: Each activation is a read-only epoch; partial loads never serve traffic.
  - Predictability over generality: Only serve query shapes with bounded cost; everything else falls back.
  - Hard Isolation: Per-tenant memory and CPU budgets; per-tenant concurrency caps; strict admission control.
  - Benefit-Driven Residency: Admit/evict shards by benefit/memory score (saved scan/CPU × heat ÷ bytes).
  - Simple, composable control plane: State machine with minimal phases: LOADING → VALIDATING → READY → RETIRING.
  - Compute: Workers must maintain in-memory datasets within strict quotas; no partial shard spill.
  - Query bounds: Per-request CPU/row/scan caps enforce predictability; cap breaches trigger OLAP fallback.
  - Schema evolution: Supported per epoch; incompatible changes require new dataset versions.

#### Limitation

- Not a System of Record: This system is a transient cache. The final, authoritative data remains in the source OLAP engines or Iceberg tables. Data loss in this layer is recoverable by reloading from the source.

- Focus on Analytical Queries (OLAP): The system is optimized for read-heavy analytical workloads, not for transactional (OLTP) read-write operations.

- Data Freshness: The data within this layer is as fresh as the last completed data pipeline. It is not designed for real-time, streaming updates.








