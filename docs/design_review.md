# Design Review (Second Pass): gRPC & Etcd Validation

## 1. Introduction & Revised Scope

This document is a second-pass design review, building upon a preliminary assessment. Based on initial feedback, the scope of this review has been narrowed to focus specifically on validating the core control plane components responsible for system consistency and linearizability:

1.  **gRPC Protocol (`control_plane_service.proto`):** A deep-dive analysis of the API contract between the Master and Workers, focusing on its robustness, clarity, and ability to prevent race conditions.
2.  **Etcd Keyspace Organization:** A validation of the proposed key structure to ensure it supports atomic operations, efficient watches, and scalable, multi-tenant state management.

The goal is to lock in these foundational designs, ensuring they are sound before proceeding with implementation.

### 1.1. Out of Scope for This Review

-   **Admission Controller & Benefit-Driven Residency:** The logic for deciding *which* data to cache (the "Benefit-Driven Residency Model") is considered an external business logic component that feeds decisions *to* the Master. The Master's role is to reliably *execute* assignments, not to formulate the caching strategy itself. Therefore, the implementation of the Admission Controller is explicitly out of scope for this core Master design document.

    > **Definition: Admission Controller and Benefit-Driven Residency Model:** This refers to a sophisticated decision-making system mandated by the PRD. Its purpose is to intelligently decide which data "epochs" (e.g., daily partitions of a table) should be loaded into the in-memory acceleration layer. Instead of just loading everything, it calculates a score for each piece of data based on its "benefit" (e.g., how frequently it's queried, how much query cost it saves) versus its "cost" (the amount of memory it consumes). Data with a high benefit/cost ratio is admitted into the cache, while low-scoring data may be ignored or evicted. This ensures the expensive in-memory resources are always used for the most valuable data.

-   **Detailed Security Implementation:** While a comprehensive security model (including mTLS, tenant authentication, and secret management) is critical for the project, it will be addressed in a separate, dedicated security review.

---

## 2. Etcd Keyspace: Validation

The etcd keyspace design proposed in `master_architecture.md` (Section 5) is reviewed here for correctness, atomicity, and scalability.

**Proposal:**
```text
/
├── masters/{master_id}
├── election/master
├── workers/{tenant_id}/{worker_id}
├── assignments/{tenant_id}/{dataset_id}/{epoch_id}
├── tenants/{tenant_id}/config
├── tenants/{tenant_id}/datasets/{dataset_id}
└── config/global
```

**Validation Points & Findings:**

-   **Structure and Tenant Isolation:**
    -   **Validation:** The hierarchical structure with `tenant_id` at a high level is **excellent**. It provides strong namespacing, allowing for efficient, tenant-scoped watches and preventing any cross-tenant data leakage at the state-store level. This design is confirmed as **sound**.
    -   **Decision:** The proposed key structure is **approved**.

-   **Worker Liveness and Leases:**
    -   **Validation:** Using leased, ephemeral keys for `/workers/{tenant_id}/{worker_id}` is the correct, idiomatic `etcd` pattern for managing member liveness. The automatic deletion of the key upon lease expiry provides a reliable failure detection mechanism for the Master. This design is confirmed as **robust**.
    -   **Decision:** The worker liveness mechanism is **approved**.

-   **Data Assignments and Atomicity:**
    -   **Validation:** The key `/assignments/{tenant_id}/{dataset_id}/{epoch_id}` cleanly represents a single unit of assignment. Storing the list of worker IDs in the value is acceptable.
    -   **Potential Issue:** The current rebalancing logic described in the pseudocode (`rebalanceOnWorkerChange`) suggests a multi-step `Get -> For-Each -> Put` process to reassign epochs from a failed worker. This is a **potential race condition**. If another event occurs while this multi-step transaction is in flight (e.g., another worker fails), the system state could become inconsistent.
    -   **Recommendation:** All state changes related to a single rebalancing event should be executed within a single, atomic `etcd` transaction (using `Txn`). For example, when reassigning an epoch, the transaction should verify the old assignment still exists and then update it to the new worker list in a single atomic operation. This guarantees that the reassignment logic is linearizable.

-   **Scalability of Watches:**
    -   **Validation:** Prefix watches are efficient in `etcd`. The Master can watch `/workers/` for cluster changes and `/assignments/` for state changes. This is a scalable pattern.
    -   **Consideration:** At extreme scale (e.g., millions of assignment keys), a single Master leader watching the entire `/assignments/` prefix could become a bottleneck. While not an immediate concern, the design should acknowledge that future scaling might require partitioning the watch space (e.g., sharding assignments by hash of dataset_id and having dedicated sub-leaders). For the current scope, the single-watch design is **acceptable**.

**Conclusion:** The etcd keyspace design is **strong and well-considered**. With the recommendation to enforce atomic transactions for all multi-step state modifications, the design is approved for implementation.

---

## 3. gRPC Protocol: Validation

The bidirectional gRPC stream defined in `control_plane_service.proto` is reviewed for clarity, correctness, and potential race conditions.

**Proposal:** A single `rpc EventStream(stream EventStreamMessage) returns (stream EventStreamMessage)` for all Master-Worker communication.

**Validation Points & Findings:**

-   **Initial Registration and Stream Liveness:**
    -   **Validation:** The protocol correctly mandates that a `RegisterEvent` must be the first message from a worker. This allows the Master to establish the worker's identity and associate the gRPC stream with the corresponding `etcd` lease. This flow is **confirmed as sound**.
    -   **Potential Issue:** The `RegisterEvent` includes the worker's address. If this address is only used for "gRPC if needed," as the comment suggests, it is largely redundant, as the primary communication channel is the stream itself. However, it may be useful for debugging or administrative purposes. This is a minor point, and the field can be retained.

-   **State Reporting and Atomicity:**
    -   **Validation:** The `DataPullStatusUpdateEvent` allows the worker to report progress on data loading. The enum `Status` (`IN_PROGRESS`, `COMPLETED`, `FAILED`) is clear and sufficient.
    -   **Race Condition:** A critical race condition exists. A worker could send `DataPullStatusUpdateEvent{status: COMPLETED}` for an epoch *just as it fails*. The Master might process the `COMPLETED` event, mark the epoch as `READY` in `etcd`, and then process the worker failure (lease expiry). The Data Gateway could momentarily route queries to a dead worker.
    -   **Recommendation:** The source of truth for an epoch's readiness must be atomic. The worker should **not** send a `COMPLETED` event. Instead, the final step of a successful data load should be for the worker to make an RPC to the Master (e.g., a new unary `FinalizeEpoch(dataset_id, epoch_id)`) or send a specific a `FinalizeEpochEvent`. Only upon receiving this specific event should the Master leader, in a single `etcd` transaction, update the assignment status to `READY` (e.g., `/assignments/... -> {"workers": [...], "status": "READY"}`). This makes the readiness of an epoch an explicit, atomic, and master-driven state change.

-   **Command and Control:**
    -   **Validation:** The `HeartbeatAckEvent` containing an `Action` is a clever way to piggyback commands onto an existing message flow.
    -   **Consideration:** As the system grows, the number of control-plane actions may increase. A generic `ControlCommand` message within the `oneof` payload, as suggested in the preliminary review, might offer better long-term extensibility than overloading the `HeartbeatAckEvent`. However, for the defined actions (`NONE`, `DRAIN`), the current design is **acceptable**.

**Conclusion:** The gRPC protocol is **largely robust**, but the state-reporting model contains a critical race condition. The protocol should be modified to make the final "READY" state transition an explicit, atomic, Master-driven action in `etcd`, rather than an implicit status reported by the worker.

---

## 4. Final Recommendation

**Recommendation: Approve with Conditions.**

This focused review validates that the core designs for the **etcd keyspace** and **gRPC protocol** are fundamentally sound and provide a strong foundation for a consistent, scalable control plane.

Approval is granted on the condition that the following two modifications are integrated into the final design and implementation:

1.  **Etcd Transactions:** All multi-step state modifications in the Master (especially for rebalancing and assignments) **must** be executed within a single, atomic `etcd` transaction (`Txn`) to prevent race conditions.

2.  **gRPC State Finalization:** The gRPC protocol **must** be changed so that a worker signals its completion of a data pull via a new, explicit `FinalizeEpoch` event or RPC. The Master, upon receiving this signal, will be solely responsible for atomically transitioning the epoch's state to `READY` in `etcd`. The `DataPullStatusUpdateEvent` should not be used for this purpose.

With these conditions met, the control plane's foundational components are approved for implementation.