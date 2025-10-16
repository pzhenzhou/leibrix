# Master System Architecture: Distributed In-Memory Acceleration Layer

## 1. Overall Architecture

The Master is the central coordination component of the distributed, memory-centric acceleration layer. It acts as a stateful control plane, orchestrating data placement and lifecycle across a cluster of Worker nodes without managing the data itself. By leveraging a distributed consensus store (etcd), it maintains the global state of the cluster, including worker status and data assignments. Its primary role is to ensure that hot data from the underlying data lakehouse (e.g., Iceberg tables, OLAP snapshots) is available in memory on the correct Workers, ready to be queried with low latency via the Data Gateway.

The system follows a **master-worker** architecture where the Master is the brain and the Workers are the high-performance data engines. The data onboarding and assignment flow is as follows:

1.  **Admission Request**: An external administrator or service interacts with the Master's `ManagementService` gRPC API to request that a specific dataset (e.g., a set of partitions from an Iceberg table) be loaded into the speed layer.
2.  **Admission Control**: The Master's internal **Admission Controller** receives this request. It connects to the underlying data source (e.g., Iceberg catalog) to resolve the request into a concrete set of data files, schema, and partition information.
3.  **Load Plan Generation**: The Admission Controller generates a detailed `LoadPlan`. This plan is an explicit, immutable recipe that tells a Worker exactly what data to pull and how to load it.
4.  **Data Assignment**: The Master determines which Worker(s) should handle the `LoadPlan` and persists this assignment in `etcd`.
5.  **Notify & Pull**: The Master sends a `DataAssignmentEvent`, containing the `LoadPlan`, over the persistent gRPC stream to the designated Workers.
6.  **Caching & Serving**: Workers pull the immutable data directly from the source as specified in the `LoadPlan`, load it into their in-memory engine (DuckDB), and report their status as `READY`. The Data Gateway can then route queries for that data epoch.

This architecture treats the in-memory layer as a **CPU cache for the data lakehouse**. It holds the hottest, most frequently accessed data subsets in a high-speed medium (RAM) to accelerate queries, while the larger, colder dataset remains in the lakehouse.

### High-Level System Diagram

```text
+----------------------+      +----------------------+
|  Admin / Controller  |----->|   ManagementService  |
+----------------------+ gRPC | (External Master API)|
                              +----------+-----------+
                                         |
+----------------------+      +----------------------+      +----------------------+
|   Client/BI Tools    |----->|     Data Gateway     |<---->|        Master        |
+----------------------+      +----------------------+      | (Coordination/etcd)  |
                                       ^                     +----------+-----------+
                                       | gRPC (Query)                   | gRPC (Event Stream)
                                       v                                v
+----------------------+      +----------------------+      +----------------------+
|    Worker 1 (RAM)    |      |    Worker 2 (RAM)    |      |    Worker N (RAM)    |
| - DuckDB Engine      |      | - DuckDB Engine      |      | - DuckDB Engine      |
| - Immutable Epoch A  |      | - Immutable Epoch B  |      | - Immutable Epoch C  |
+----------------------+      +----------------------+      +----------------------+
        ^ (Data Pull)                   ^ (Data Pull)                   ^ (Data Pull)
        |                               |                               |
        +-------------------------------+-------------------------------+
                                        |
                                        v
                          +--------------------------+
                          |   Data Lakehouse         |
                          | (Iceberg / OLAP Source)  |
                          +--------------------------+
```

## 2. Master Cluster Management

To ensure high availability and prevent a single point of failure, the Master is deployed as a distributed cluster. It leverages an embedded `etcd` instance for all coordination, state management, and consensus, aligning with Go's principles of simplicity and robust concurrency.

### Master Membership and Leader Election

-   **Membership**: Each Master node registers itself in `etcd` upon startup by creating a key under a well-defined prefix (e.g., `/masters/{master_id}`). This registration includes its gRPC endpoint for intra-cluster communication if needed.
-   **Leader Election**: The cluster elects a single active leader using `etcd`'s built-in election API, which is based on the RAFT consensus algorithm. The leader is responsible for making all critical decisions, such as data assignments and worker notifications. Follower nodes remain on standby, ready to take over if the leader fails. They use `etcd` watches to observe state changes made by the leader and maintain a consistent view.
-   **Failover**: If the leader node fails, its `etcd` session will time out, and its leadership lease will expire. The `etcd` election library ensures that one of the followers will be promptly elected as the new leader, minimizing downtime.

### Golang Pseudocode: Leader Election

This snippet demonstrates how a Master node would participate in leader election using the official `etcd` client library.

```go
package master

import (
	"context"
	"fmt"
	"time"

	"go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// becomeLeader attempts to acquire leadership and blocks until it is lost.
// It runs the provided `leaderLoop` function which contains the leader's duties.
func becomeLeader(ctx context.Context, client *clientv3.Client, masterID string) error {
	s, err := concurrency.newSession(client)
	if err != nil {
		return fmt.Errorf("failed to create etcd session: %w", err)
	}
	defer s.Close()

	// The election prefix ensures all masters compete for the same lock.
	e := concurrency.NewElection(s, "/election/master")

	// Campaign blocks until this node is elected.
	if err := e.Campaign(ctx, masterID); err != nil {
		return fmt.Errorf("failed to campaign for leadership: %w", err)
	}
	fmt.Printf("%s elected as leader\n", masterID)

	// Leadership is held until the context is canceled or the session is lost.
	// We run the leader's main logic loop here.
	leaderLoop(ctx)

	// Resign leadership gracefully if the context is done.
	defer func() {
		if resignErr := e.Resign(context.Background()); resignErr != nil {
			fmt.Printf("failed to resign leadership: %v\n", resignErr)
		}
	}()

	fmt.Printf("%s lost leadership\n", masterID)
	return nil
}

// leaderLoop is the primary function executed by the elected leader.
func leaderLoop(ctx context.Context) {
	fmt.Println("Leader loop started. Performing leadership duties.")
	// Example duties: watch for new data, assign to workers, monitor cluster.
	<-ctx.Done()
	fmt.Println("Leader loop stopping.")
}

```

## 3. Worker Status Management

The Master is solely responsible for tracking the health and status of all Worker nodes. Workers are designed to be simple and unaware of `etcd`; they communicate with the Master exclusively via a persistent, bidirectional gRPC stream. This event-driven approach simplifies the communication model and ensures real-time state synchronization.

-   **Connection & Registration**: On startup, a Worker establishes a long-lived gRPC stream to the Master by calling the `EventStream` RPC. The first event it sends on this stream is a `RegisterEvent`. The Master (leader) accepts this event, creates a new lease in `etcd`, associates it with the Worker's key (e.g., `/workers/{tenant_id}/{worker_id}`), and sends back a `RegistrationAckEvent`.
-   **Heartbeat**: The Worker then periodically sends `HeartbeatEvent` messages on the stream to signal its liveness. On receiving a heartbeat, the Master refreshes (keeps alive) the Worker's lease in `etcd`.
-   **Failure Detection**: If the gRPC stream breaks or the Master stops receiving heartbeats within the lease's Time-To-Live (TTL), the lease expires. `etcd` automatically deletes the associated Worker key. The Master uses an `etcd` watch to detect this deletion and marks the Worker as `offline`. This triggers a rebalancing of any data assignments that the failed Worker held.

### Sequence Flow: Worker Event Stream

```text
Worker                  Master (Leader)               etcd
  |                        |                           |
  |--- EventStream() ------>| (Stream Established)      |
  |                        |                           |
  |--- RegisterEvent ------>|                           |
  |                        |-- CreateLease(TTL)  ------>|
  |                        |<-- LeaseID ----------------|
  |                        |-- Put(/workers/{id}, LeaseID) ->|
  |<-- RegistrationAckEvent-|                           |
  |                        |                           |
  | (Periodically)         |                           |
  |--- HeartbeatEvent ---->|                           |
  |                        |-- KeepAlive(LeaseID) ---->|
  |                        |                           |
  | (Stream breaks)        |                           |
  |                        | (Lease expires)           |
  |                        |                           |-- Key /workers/{id} deleted
  |                        |<-- Watch Event -----------|
  |                        |                           |
  |                        | (Mark worker offline,     |
  |                        |  trigger rebalancing)     |
  |                        |                           |
```

## 4. Admission Control and Data Onboarding

The **Admission Controller** is a logical component within the Master responsible for managing the data lifecycle in the speed layer. It acts as the gatekeeper, translating high-level requests into concrete, executable `LoadPlan`s for the workers.

-   **Entry Point**: The controller is exposed via the external `ManagementService` gRPC API. This allows administrators or automated systems to declaratively state which data should be accelerated.
-   **Responsibilities**:
    1.  **Request Validation**: It validates incoming `AdmitDatasetRequest` messages, ensuring the specified tenant and data source are valid.
    2.  **Source Interrogation**: It connects to the external data source (e.g., an Iceberg catalog) to resolve the request. For example, it translates a request for "the last 7 days of the `sales` table" into a specific list of manifest files, data files, and the corresponding table schema for that snapshot.
    3.  **LoadPlan Generation**: It constructs the detailed, immutable `LoadPlan`. This plan contains all the physical information a worker needs, removing any ambiguity and ensuring workers do not need to contain complex source-specific logic.
    4.  **Quota Enforcement**: Admission applies per-tenant and global memory budgets before a `LoadPlan` is persisted. It consults the Capacity Planner (see Module Map) to ensure the resulting footprint keeps the cluster within reserved and burst allocations.
    5.  **Idempotency & Auditing**: Every request receives a deterministic identifier. The controller persists its status transitions (`PENDING`, `PLANNED`, `ASSIGNED`, `FAILED`) so callers can safely retry without duplicating work. Audit trails are exported via the Metrics/Observability module.

The Admission Controller produces two primary artifacts that other modules consume:

-   `LoadPlan`: Immutable per-dataset instruction set that enumerates object storage URIs, schema descriptors, partition filters, and desired replication level.
-   `DatasetSpec`: Canonical metadata for the dataset epoch (snapshot ID, version, TTL) which becomes the key for routing queries and cache invalidation.

## 5. RPC Connection Management and Reliability Policy

All communication between the Master and external actors (Admins and Workers) happens via gRPC. The Master provides two public services: `ManagementService` (control plane operations) and `EventStreamService` (worker coordination). The platform enforces consistent connection, retry, and timeout strategies to avoid runaway resource usage or cascading failures.

### 5.1 ManagementService (Unary RPCs)

-   **Client Expectations**: External callers (CLI, automation) issue unary RPCs. Each call must include a tenant-scoped authentication token and an idempotency key.
-   **Server Policies**:
    -   **Deadlines**: The server enforces a default 5s deadline for admission-related reads (catalog lookups may be longer, up to 30s) and a 60s deadline for long-running mutations (load planning). Requests exceeding server-side deadlines receive `DEADLINE_EXCEEDED`.
    -   **Retry Semantics**: The API designates retryable errors using canonical status codes. `UNAVAILABLE`, `ABORTED`, and `RESOURCE_EXHAUSTED` return gRPC trailers containing a `retry-after-ms` hint for exponential backoff (base 200ms, jittered, capped at 5s).
    -   **Validation Failures**: Invalid parameters return `INVALID_ARGUMENT` with structured field violations.
    -   **Quota Rejections**: When admission would exceed policy budgets, the server returns `FAILED_PRECONDITION` and attaches the current utilization snapshot in the error metadata.
-   **Server Implementation Notes**:
    -   Requests are routed through a `UnaryConnectionManager` (see Module Map) that tracks active streams per tenant, limits concurrency, and exposes metrics (`grpc_server_handled_total`, `grpc_server_msg_received_total`).
    -   Mutual TLS is mandatory; certificates are rotated by the Security & Identity module.

### 5.2 EventStreamService (Bidirectional Streams)

-   **Connection Lifecycle**:
    1.  Workers dial the Master using mutual TLS and establish a stream with `OpenEventStream`.
    2.  The Master authenticates the worker (tenant-scoped identity) and issues a session token bound to the gRPC stream.
    3.  The stream is registered in the `StreamRegistry`, which maintains a mapping of `WorkerID -> StreamHandle` and tracks last-heartbeat timestamps.
-   **Timeouts & Heartbeats**:
    -   Workers must send a heartbeat every `heartbeat_interval` (default 5s). Missing two intervals triggers a soft warning; missing three causes the Master to proactively close the stream with `DEADLINE_EXCEEDED`.
    -   The Master also sends keepalive pings (HTTP/2 PING) every 15s to detect half-open TCP connections.
-   **Backpressure & Flow Control**:
    -   Load assignments are pushed with credit-based flow control. Each worker advertises its `MaxInFlightAssignments`. The Master never sends more than this number without receiving an `AckEvent`.
    -   If the worker's in-memory queue fills, it can send a `ThrottleEvent` to temporarily reduce pressure. The Master responds with a `RESOURCE_EXHAUSTED` status for new data onboarding attempts until the worker recovers.
-   **Error Handling**:
    -   Recoverable stream-level errors use `UNAVAILABLE` with a retry delay hint. Workers back off exponentially (base 500ms, cap 8s) before reconnecting.
    -   Non-retryable errors (authentication failure, protocol violation) return `PERMISSION_DENIED` or `FAILED_PRECONDITION`, and the worker must re-register manually.
    -   When the Master plans maintenance, it issues a `GracefulShutdownEvent` and closes the stream with status `OK`. Workers flush outstanding work and reconnect to the new leader.

### 5.3 Error Taxonomy and Observability

-   All gRPC handlers attach a `correlation-id` in response headers to link to structured logs.
-   Retries, timeouts, and stream closures emit events into the Observability module to feed dashboards (per-tenant error rates, p99 latencies, stream churn).
-   The `ConnectionPolicy` configuration (deadlines, heartbeat interval, retry caps) is stored in the Config Store module and supports dynamic reload without downtime.

## 6. Master Module Map and Package Layout

The Master codebase is organized into Go packages that correspond to clear control-plane responsibilities. Packages depend on one another through narrow interfaces defined in an `internal/api` layer to keep the surface well-encapsulated.

| Package | Responsibility | Key Interfaces/Structs | Collaborators |
|---------|----------------|------------------------|---------------|
| `cmd/master` | Binary entry point; wires configuration, logging, telemetry, and starts the runtime. | `main()`, `bootstrapRuntime()` | Imports `runtime`, `config`, `transport/grpcserver`. |
| `internal/runtime` | Supervises service startup/shutdown, leader election loop, and dependency injection. | `Runtime`, `Component`, `Lifecycle` | `cluster/election`, `transport`, `modules/*`. |
| `internal/cluster/election` | Leader election and membership via etcd. | `LeaderElector`, `Candidate`, `LeaseConfig` | Consumed by `runtime`. |
| `internal/cluster/workers` | Worker directory, stream registry, heartbeat processing, failover handling. | `WorkerDirectory`, `StreamRegistry`, `HeartbeatMonitor` | Uses `store`, `transport/grpcserver`. |
| `internal/admission` | Validates dataset requests, generates `LoadPlan`s, applies quota policies. | `AdmissionController`, `Planner`, `QuotaEvaluator` | Collaborates with `modules/catalog`, `modules/capacity`. |
| `internal/assignment` | Chooses placement strategy, issues `DataAssignmentEvent`s, manages rebalancing. | `AssignmentStrategy`, `AssignmentManager` | Depends on `cluster/workers`, `store`, `modules/capacity`. |
| `internal/transport/grpcserver` | Hosts gRPC services, enforces connection policies, exposes interceptors. | `UnaryConnectionManager`, `StreamRegistry`, `AuthInterceptor` | Interfaces with `api/grpc` definitions, `security`. |
| `internal/api/grpc` | Generated protobuf code and thin adapters exposing request/response types. | `pb.ManagementServiceServer`, `pb.EventStreamServiceServer` | Implemented by `modules/api`. |
| `internal/modules/api` | Implements gRPC service handlers delegating to admission, assignment, worker modules. | `ManagementServer`, `EventStreamServer` | Calls into `admission`, `assignment`, `cluster/workers`. |
| `internal/modules/catalog` | Abstractions over external metadata/catalog systems. | `CatalogClient`, `SnapshotResolver` | Used by `admission`. |
| `internal/modules/capacity` | Tracks memory budgets, worker capacity, load shedding policies. | `CapacityPlanner`, `BudgetLedger` | Consulted by `admission`, `assignment`. |
| `internal/modules/store` | Thin wrappers over etcd (KV, leases, watches). | `KVStore`, `LeaseManager`, `Txn` | Used by `cluster` and `assignment`. |
| `internal/security` | mTLS, token validation, RBAC decisions. | `Authenticator`, `Authorizer`, `CertificateRotator` | Intercepts gRPC requests, integrates with `transport`. |
| `internal/config` | Typed configuration loader supporting dynamic reload. | `ConfigProvider`, `Watcher` | Used by all modules during bootstrap. |
| `internal/observability` | Metrics, logging, tracing, audit events. | `MetricsRegistry`, `AuditEmitter` | Receives hooks from all modules. |

Interfaces exposed across packages remain in `internal` to prevent leaking implementation details to downstream repos. Each package includes an `interfaces.go` file that enumerates exported contracts and a corresponding `service.go` implementing the logic.

### Core Abstractions

-   **`LeaderElector`** – `Campaign(ctx) (Leadership, error)` returns a handle exposing `Done() <-chan struct{}` and `Resign(ctx)` so the runtime can coordinate orderly shutdowns.
-   **`WorkerDirectory`** – Provides `Upsert(info WorkerInfo)`, `Remove(workerID string)`, and `Subscribe(ctx) <-chan WorkerEvent` to keep consumers in sync with worker liveness changes.
-   **`AdmissionService`** – Implements unary gRPC handlers (`AdmitDataset`, `ListDatasets`) and emits internal `AdmissionJob` objects containing tenant context, source descriptors, and policy hints.
-   **`LoadPlanner`** – Accepts `AdmissionJob` inputs and materializes `LoadPlanDescriptor` outputs (etcd keys, object manifests, replication policy) while deduplicating in-flight plans.
-   **`AssignmentStrategy`** – Defines `Select(plan LoadPlanDescriptor, candidates []WorkerInfo) ([]Assignment, error)` enabling pluggable placement policies (round-robin, capacity-aware, replica fan-out).
-   **`EventSink`** – Abstracts streaming communication with workers using `Send(workerID, msg)` and `Broadcast(tenantID, msg)` that wrap retry/backoff semantics consistent with the connection policy.
-   **`ConfigProvider`** – Supplies strongly typed tenant/global configuration snapshots via `GetTenant(ctx, id)` and watch channels for hot reload (`WatchTenant(ctx, id) <-chan TenantConfig`).
-   **`QuotaManager`** – Evaluates proposed `LoadPlanDescriptor`s against tenant and cluster budgets (`Evaluate(plan) (Decision, error)`) leveraging worker utilization telemetry.
-   **`AuditLogger`** – Emits append-only audit events for admissions, assignments, and failovers with guaranteed durability (e.g., persisted to an immutable log sink).

### Lifecycle Integration

Every module implements the `Component` interface:

```go
type Component interface {
        Start(ctx context.Context) error
        Stop(ctx context.Context) error
}
```

The `Runtime` coordinates component startup respecting dependencies (e.g., `transport/grpcserver` starts after `security` has loaded credentials). Shutdown is graceful: the runtime first stops the gRPC server (preventing new connections), then flushes outstanding assignments, finally relinquishing leadership via the election component.

## 7. Extensibility Considerations
-   **Decoupling**: This design decouples the workers from the specifics of the data lakehouse. Workers are simple "data loaders and queryers"; they only need to understand the `LoadPlan` format, not how to interact with an Iceberg catalog or an OLAP database.

## 8. Data Assignment and Sharding

The system does not perform traditional database sharding. Instead, it manages the assignment of **immutable data epochs**, as defined by a `LoadPlan`, to workers. The process is initiated by the Admission Controller.

-   **Assignment Logic**: Once a `LoadPlan` is generated, the Master leader selects available Workers based on a load balancing strategy.
    -   **Round-Robin**: Simple and effective for homogenous clusters.
    -   **Hash-Based**: A consistent hashing algorithm can be used to map a data epoch (e.g., hash of `{table_name, date_partition}`) to a Worker. This minimizes reassignments when Workers are added or removed.
    -   **Capacity-Aware**: The Master considers worker capacity (CPU, available memory) reported in `HeartbeatEvent` messages to make more intelligent assignments.
-   **Rebalancing**: The Master watches the `/workers/` prefix in `etcd`.
    -   **Worker Added**: When a new worker key appears, the Master may trigger a rebalancing to offload some data assignments from existing workers to the new one to distribute load.
    -   **Worker Removed**: When a worker key is deleted (due to failure), the Master reassigns its data epochs to other healthy workers.
-   **Persistence**: All data assignments are persisted in `etcd` under a key like `/assignments/{tenant_id}/{dataset_id}/{epoch_id}`. The value would contain the `LoadPlan` and the list of `worker_id`s responsible for it.

### Golang Pseudocode: Data Assignment

```go
package master

import (
	"context"
	"errors"
	"fmt"

	"go.etcd.io/etcd/client/v3"
)

// Naive round-robin assignment for demonstration.
var nextWorkerIndex = 0

// assignDataToWorker selects a worker and persists the assignment in etcd.
func assignDataToWorker(ctx context.Context, client *clientv3.Client, availableWorkers []string, dataEpochID string) error {
	if len(availableWorkers) == 0 {
		return errors.New("no available workers to assign data")
	}

	// Simple round-robin logic
	workerID := availableWorkers[nextWorkerIndex%len(availableWorkers)]
	nextWorkerIndex++

	assignmentKey := fmt.Sprintf("/assignments/%s", dataEpochID)
	assignmentValue := workerID // In reality, could be a JSON with replica info

	_, err := client.Put(ctx, assignmentKey, assignmentValue)
	if err != nil {
		return fmt.Errorf("failed to persist assignment in etcd: %w", err)
	}

	// After persisting, notify the worker via the gRPC stream
	// stream.Send(&EventStreamMessage{payload: &DataAssignmentEvent{...}})
	return nil
}

// rebalanceOnWorkerChange is triggered by an etcd watch when a worker is added/removed.
func rebalanceOnWorkerChange(ctx context.Context, client *clientv3.Client, failedWorkerID string) {
	// 1. Find all data epochs assigned to the failed worker.
	//    resp, err := client.Get(ctx, "/assignments/", clientv3.WithPrefix()) ...
	// 2. For each epoch, re-assign it to a healthy worker.
	//    assignDataToWorker(...)
	// 3. Delete the old, invalid assignment.
}
```

## 9. Etcd Key Organization

A well-defined `etcd` key structure is critical for discoverability, atomic operations, and efficient watches. We adopt a hierarchical, tenant-aware key schema.

```text
/
├── masters/
│   └── {master_id} -> {"address": "host:port"} (Ephemeral, for membership)
│
├── election/
│   └── master -> {leader_id} (Leased, for leader election)
│
├── workers/
│   └── {tenant_id}/
│       └── {worker_id} -> {"address": "host:port", "capacity": {...}} (Leased, for liveness)
│
├── assignments/
│   └── {tenant_id}/
│       └── {dataset_id}/
│           └── {epoch_id} -> {"workers": ["worker_a", "worker_b"], "status": "READY", "load_plan": {...}}
│
├── tenants/
│   └── {tenant_id}/
│       ├── config -> {"memory_quota_gb": 1024, "cpu_quota": 8}
│       └── datasets/
│           └── {dataset_id} -> {"source_type": "iceberg", "source_uri": "..."}
│
└── config/
    └── global -> {"log_level": "info", "default_ttl_sec": 30}

```

-   `/masters/{master_id}`: Used for Master cluster membership.
-   `/election/master`: A single key used for the leader election protocol.
-   `/workers/{tenant_id}/{worker_id}`: Tracks live workers per tenant. Leased keys ensure automatic cleanup on failure.
-   `/assignments/{tenant_id}/{dataset_id}/{epoch_id}`: The core data mapping. Stores the `LoadPlan` and the worker assignments for a given data epoch.
-   `/tenants/{tenant_id}`: Stores all configuration and metadata for a specific tenant, such as resource quotas and source definitions.
-   `/config/global`: For cluster-wide settings.

## 10. gRPC Protocols

gRPC is the communication backbone for the system. Two primary services are defined:
-   `ControlPlaneService`: For internal, real-time communication between the Master and Workers.
-   `ManagementService`: For external, administrative control over the Master.

### `control_plane_service.proto`

```protobuf
syntax = "proto3";

package speedlayer.v1;

option go_package = "github.com/leibrix/speedlayer/api/v1";

// ControlPlaneService defines the single, persistent stream for all
// Master-Worker communication.
service ControlPlaneService {
  // EventStream is a long-lived, bidirectional stream. The first message from
  // a worker MUST be a RegisterEvent.
  rpc EventStream(stream EventStreamMessage) returns (stream EventStreamMessage);
}

// EventStreamMessage is the union type for all messages exchanged
// between the Master and a Worker.
message EventStreamMessage {
  // A unique identifier for the event, used for logging and correlation.
  string event_id = 1;
  // Tenant and Worker IDs provide context for the event.
  string tenant_id = 2;
  string worker_id = 3;

  oneof payload {
    // ---- Events initiated by the Worker ----

    // Sent once by the worker upon connecting to register itself.
    RegisterEvent register_event = 4;
    // Sent periodically by the worker to signal liveness.
    HeartbeatEvent heartbeat_event = 5;
    // Sent by the worker to update the master on the status of a data pull.
    DataPullStatusUpdateEvent data_pull_status_update = 6;

    // ---- Events initiated by the Master ----

    // Sent in response to a successful RegisterEvent.
    RegistrationAckEvent registration_ack = 7;
    // Sent to a specific worker to instruct it to pull a data epoch.
    DataAssignmentEvent data_assignment = 8;
    // Sent in response to a HeartbeatEvent, can also carry commands.
    HeartbeatAckEvent heartbeat_ack = 9;
  }
}

// --- Worker -> Master Event Payloads ---

message RegisterEvent {
  // The gRPC address the Master can use to reach this worker if needed
  // (though communication is primarily over this stream).
  string address = 1;
  WorkerCapacity capacity = 2;
}

message HeartbeatEvent {
  // Current resource utilization on the worker.
  ResourceStatus status = 1;
}

message DataPullStatusUpdateEvent {
  string dataset_id = 1;
  string epoch_id = 2;
  enum Status {
    UNKNOWN = 0;
    IN_PROGRESS = 1;
    COMPLETED = 2;
    FAILED = 3;
  }
  Status status = 3;
  string error_message = 4; // Populated if status is FAILED.
}

// --- Master -> Worker Event Payloads ---

message RegistrationAckEvent {
  // The interval in seconds at which the worker should send heartbeats.
  int32 heartbeat_interval_seconds = 1;
}

message DataAssignmentEvent {
  string dataset_id = 1;
  string epoch_id = 2;
  // A structured plan describing where to get the data and how to load it.
  LoadPlan load_plan = 3;
}

message HeartbeatAckEvent {
  // The master can use this to instruct the worker to take action.
  enum Action {
    NONE = 0;
    DRAIN = 1; // Stop accepting new queries and prepare for shutdown.
  }
  Action requested_action = 1;
}

// --- Common Sub-Messages ---

message WorkerCapacity {
  int64 memory_bytes = 1;
  int32 cpu_cores = 2;
}

message ResourceStatus {
  int64 memory_used_bytes = 1;
  float cpu_load_avg_5m = 2;
}

message LoadPlan {
  // A unique identifier for this loading task, correlating to a specific epoch.
  string plan_id = 1;

  // The specific data source this plan targets.
  DataSource source = 2;

  // The name of the table as it should be created or replaced in the worker's
  // in-memory database (e.g., DuckDB).
  string destination_table_name = 3;

  // The schema for the data being loaded, serialized as an Arrow IPC schema.
  bytes arrow_schema = 4;
}

message DataSource {
  oneof source_type {
    IcebergSource iceberg = 1;
    OlapSource olap = 2;
  }
}

message IcebergSource {
  string table_name = 1;
  string snapshot_id = 2;
  // The concrete set of physical data files to be loaded by the worker.
  // This is resolved by the master's admission controller from the manifest.
  repeated DataFile files = 3;
}

message OlapSource {
  // Data Source Name (DSN) for the worker to connect to the OLAP source.
  string dsn = 1;
  // The exact, executable query to generate the data snapshot.
  // This is constructed by the Master's Admission Controller.
  string snapshot_query = 2;

  // --- The following fields provide logical context for the worker ---
  // They can be used for logging, metrics, or schema validation.
  string catalog = 3;
  string database = 4;
  string table = 5;
  // The specific partition that this query targets.
  map<string, string> partition_spec = 6;
}

message DataFile {
  // The fully qualified URI to the data file (e.g., "s3://bucket/path/file.parquet").
  string uri = 1;
  // The format of the file.
  string format = 2; // "parquet", "orc", etc.
  // The size of the file in bytes, useful for scheduling and capacity management.
  int64 size_bytes = 3;
  // Partition values associated with this file.
  map<string, string> partition_values = 4;
}
```

### `management_service.proto`

```protobuf
syntax = "proto3";

package speedlayer.v1;

option go_package = "github.com/leibrix/speedlayer/api/v1";

// ManagementService provides an external API for controlling the speed layer.
// This is the primary entry point for the Admission Controller logic.
service ManagementService {
  // AdmitDataset instructs the Master to load a specific dataset, or a partition
  // of it, into the speed layer. The Master will resolve this logical request
  // into a concrete LoadPlan and assign it to workers.
  rpc AdmitDataset(AdmitDatasetRequest) returns (AdmitDatasetResponse);
}

message AdmitDatasetRequest {
  string tenant_id = 1;
  // A user-defined name for this dataset, used for tracking and identification.
  string dataset_id = 2;

  // A list of specific partitions to load. If empty, the latest snapshot or
  // the entire table may be loaded, depending on the source type.
  repeated PartitionSpec partitions_to_load = 3;

  // Information about the source table.
  DataSourceIdentifier source = 4;
}

message AdmitDatasetResponse {
  // A unique identifier for the data epoch that will be created for this request.
  string epoch_id = 1;
  // A message indicating that the admission request is being processed.
  string message = 2;
}

message PartitionSpec {
  // A key-value representation of the partition to load.
  // Example: {"date": "2025-10-12", "country": "US"}
  map<string, string> values = 1;
}

message DataSourceIdentifier {
   oneof source_type {
    IcebergTable iceberg_table = 1;
    OlapTable olap_table = 2;
  }
}

message IcebergTable {
  string catalog = 1;
  string database = 2;
  string table = 3;
}

message OlapTable {
  string instance = 1;
  string database = 2;
  string table = 3;
}
```

## 11. Multi-Tenancy Support

Multi-tenancy is a first-class citizen in the architecture, enforced at multiple levels by the Master. The central principle is that **a Worker belongs exclusively to one Tenant at a time**. This ensures strict resource isolation.

-   **Data Isolation**: All data assignments in `etcd` are namespaced by `tenant_id`, making it impossible for one tenant's configuration to affect another.
-   **Resource Quotas**: Tenant configurations stored under `/tenants/{tenant_id}/config` define hard limits on resources (e.g., total memory, CPU cores). The Master's assignment logic respects these quotas, refusing to assign new data epochs if a tenant would exceed its allocation.
-   **Worker Allocation**: A tenant is mapped to a pool of one or more workers. The `tenant_id` is a mandatory field in the worker's initial `RegisterEvent` and is used to scope its key in `etcd` (`/workers/{tenant_id}/{worker_id}`). This prevents cross-tenant data assignments.
-   **gRPC Stream Context**: The `tenant_id` is present in every `EventStreamMessage`, ensuring that all communication is correctly scoped and authorized. The Master's gRPC handler will validate that the worker is registered to the tenant it claims to represent.

## 12. Additional Details

### Configuration Management

-   **Source**: All static configuration for tenants and global settings is stored directly in `etcd`.
-   **Hot-Reload**: The Master uses `etcd` watches on its configuration prefixes (e.g., `/config/global`, `/tenants/`). When a config value is updated, the watch is triggered, and the Master reloads its configuration in memory without requiring a restart.

### Logging and Monitoring

-   **Logging**: The Master will use a structured logging library like `uber-go/zap`. Logs will be written as JSON and include contextual fields like `tenant_id`, `worker_id`, and `trace_id` to facilitate debugging in a distributed environment.
-   **Monitoring**: The Master will expose key metrics in a Prometheus-compatible format.
    -   `master_leader_status`: 1 if the current node is leader, 0 otherwise.
    -   `master_workers_registered_total`: A gauge of healthy workers per tenant.
    -   `master_data_assignment_duration_seconds`: A histogram of time taken to assign data.
    -   `etcd_client_latency_seconds`: Latency for `etcd` operations.

These interfaces keep the Master extensible. For example, swapping the `AssignmentStrategy` or adding a new catalog connector only requires wiring in a new implementation without touching the core orchestration loop. Similarly, isolating the gRPC event handling behind `EventSink` allows unit testing of scheduling logic without standing up real streams.

### Sample YAML Configuration (for a Master node's bootstrap)

```yaml
# master-config.yaml
# Used for initial startup before connecting to etcd.

# Unique identifier for this master node.
master_id: "master-node-01"

# Etcd client configuration.
etcd:
  endpoints: ["10.0.1.10:2379", "10.0.1.11:2379", "10.0.1.12:2379"]
  # TLS for etcd connection can be configured here.
  # tls:
  #   cert_file: "/etc/master/tls/etcd.crt"
  #   key_file: "/etc/master/tls/etcd.key"
  #   ca_file: "/etc/master/tls/etcd-ca.crt"

# gRPC server configuration for listening to Workers.
grpc_server:
  listen_address: "0.0.0.0:9090"
  # mTLS for the gRPC server will be configured here later.
  # tls:
  #   cert_file: "/etc/master/tls/grpc.crt"
  #   key_file: "/etc/master/tls/grpc.key"
  #   ca_file: "/etc/master/tls/tenant-ca.crt"

# Logging configuration.
logging:
  level: "info" # Can be overridden by global config in etcd.
  format: "json"
```
