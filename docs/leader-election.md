# Leader Election in Leibrix

This document describes how leader election works in Leibrix today, how the implementation is structured, what correctness properties it relies on, and how callers should handle failures.

## Scope

Leibrix does not implement a custom Raft election algorithm. Instead, each node runs an embedded etcd server and uses the etcd concurrency primitives for control-plane leader election.

The implementation is split across:

- `cmd/main.go`: startup and shutdown ordering
- `internal/cluster/server.go`: embedded etcd lifecycle
- `internal/cluster/election.go`: election, membership registration, leader and membership watches
- `internal/cluster/cluster_event.go`: leader and membership event model, including fencing tokens
- `internal/cluster/membership_listener.go`: adapter from callback-style watches to a queue
- `internal/api/grpc/leadership.go`: gRPC-facing leadership contract
- `internal/api/grpc/lanuch_grpc_server.go`: service health and worker-session behavior on leadership changes
- `internal/api/grpc/control_plane_service.go` and `internal/api/grpc/management_service.go`: RPC gating

## Architecture

### Runtime topology

Each Leibrix control-plane node starts three layers in order:

1. Embedded etcd (`LeibrixNodeServer`)
2. Leader election (`LeibrixLeaderElection`)
3. gRPC services (`LeibrixGRPCServer`)

This ordering matters. Election requires a reachable etcd cluster, and the gRPC layer should not accept leader-only work before leadership state is available.

### Data model in etcd

The election code uses two logical key spaces:

- Election prefix: `/leibri.io/cluster/leader-election`
- Membership prefix: `/leibri.io/cluster/members/`

The current leader is represented by an etcd election key under the election prefix. Cluster membership is represented by one JSON-encoded `MemberNode` record per node under the membership prefix.

### Single-lease design

The critical design choice is that leader election and membership registration share the same etcd session lease.

At startup, `LeibrixLeaderElection.Start` creates one `concurrency.Session` with a TTL of 15 seconds and uses it for both:

- `concurrency.NewElection(session, electionKey)`
- `client.Put(memberKey, memberJSON, clientv3.WithLease(session.Lease()))`

This gives election ownership and membership presence the same lifetime. If the session expires or is closed, both keys disappear together.

That avoids the most dangerous inconsistency in this layer: a node that still appears in cluster membership after it has already lost the lease that protected its leadership claim.

### Concurrent loops

After startup, the election service runs three goroutines:

- `campaign(ctx)`: competes to become leader using `Election.Campaign`
- `observeLeader(ctx)`: passively watches leader changes using `Election.Observe`
- `observeMembers(watchChan)`: watches membership changes under `/members/`

The roles are intentionally separated:

- `campaign` owns active participation in election
- `observeLeader` gives every node, including followers, a convergent view of who the leader is
- `observeMembers` publishes joins, updates, and removals independently of leadership

### Event delivery

Election results are pushed to registered listeners through the `Listener` interface:

- `OnLeaderChange(LeaderEvent)`
- `OnMembershipChange(MembershipEvent)`

Each watcher gets a dedicated bounded sink channel. The election service fan-outs to all listeners without blocking the main election paths. If a listener is too slow and its channel fills up, the event is dropped and the system logs the drop instead of stalling leadership handling.

`MembershipListener` is the bridge from this callback model to an MPMC queue for downstream consumers that prefer pull-based processing.

### gRPC integration

The gRPC layer depends only on the `LeadershipProvider` abstraction:

- `IsLeader() bool`
- `LeaderName() string`
- `Watch(listener cluster.Listener) func()`

This keeps transport code decoupled from the concrete etcd-backed election implementation.

The integration has two parts:

- Admission path: `requireLeader` rejects leader-only RPCs on followers
- Runtime path: `LeibrixGRPCServer` watches leadership changes, updates health status, and closes active worker sessions when the local node loses leadership

In practice:

- `ManagementService` rejects writes such as `AdmitDataset` and `UpsertTenantQuota` on non-leaders
- `ControlPlaneService.CoordinateWorker` rejects new worker streams on non-leaders and terminates existing streams when leadership is lost

## Core abstractions

### `MemberNode`

`MemberNode` describes a cluster member:

- node name
- advertised address
- listen address
- metadata
- role (`leader`, `follower`, `learner`, `candidate`)

The role is local process state, not a source of truth stored in etcd.

### `LeibrixLeaderElection`

`LeibrixLeaderElection` owns:

- the etcd client
- the etcd session
- the etcd election object
- the local `MemberNode`
- listener registration and fan-out
- the last observed leader name

Important methods:

- `Start(ctx)`: create session, register membership, and start background loops
- `Close()`: cancel loops, close the session, revoke the shared lease, close listeners, close the client
- `Watch(listener)`: subscribe to leader and membership events
- `Members()`: list current members from etcd
- `IsLeader()` and `LeaderName()`: expose local leadership state to callers

### `LeaderEvent`, `MembershipEvent`, and `Event`

`LeaderEvent` represents:

- `EvtLeaderElected`
- `EvtLeaderResigned`
- `EvtLeaderExpired`

`MembershipEvent` represents:

- `EvtMemberJoined`
- `EvtMemberLeft`
- `EvtMemberUpdated`

`Event` is the normalized wrapper used by `MembershipListener` when enqueueing events for consumers.

### `FencingToken`

`FencingToken` is the monotonic epoch for a leadership term. It is derived from the etcd election key's `CreateRevision`.

This is the split-brain defense for downstream components. Consumers that receive state-changing commands from a leader must reject commands carrying a lower fencing token than the highest token they have already accepted.

Without this, a previously isolated leader could continue issuing stale commands after a new leader has already been elected.

## Correctness

### Safety: at most one active leader term

Leibrix relies on etcd's election primitive for the core exclusivity property. Only one session can hold the winning election key for the prefix at a time. Leibrix does not reimplement leader arbitration itself.

Locally, `campaign` may mark the node as leader as soon as `Campaign` returns, but cluster-wide leader notifications are still published from `observeLeader`, which watches the shared etcd state rather than trusting only local process state.

### Safety: membership and leadership cannot drift apart silently

The shared lease between the election key and the membership key is the main local correctness property.

If the node:

- shuts down cleanly
- loses connectivity long enough for the session to expire
- crashes and stops renewing the session

then both of these are cleaned up together:

- the leadership claim
- the member registration

This eliminates a class of ghost-member and stale-leader metadata bugs.

### Safety: stale leaders are fenced

Each `EvtLeaderElected` carries the etcd `CreateRevision` as `LeaderEvent.Epoch`.

Because etcd revisions are monotonic cluster-wide, a new leader always gets a strictly newer fencing token than an old one. Downstream state machines can therefore reject old commands even if an obsolete leader is still alive and attempting to act.

### Liveness: retries on campaign failure

If `Election.Campaign` fails, the code logs the error, sleeps for `leaderChangeRetry` (5 seconds), and tries again while the context is still active.

This means transient etcd or transport failures do not permanently wedge the election loop.

### Liveness: followers learn leadership through observation

Every node runs `observeLeader`, not just the current leader. Followers therefore learn the current leader through the same etcd-backed source of truth rather than through side channels or local guesses.

### Operational correctness at the API layer

The gRPC server reacts to leadership changes, not just startup state:

- when the local node becomes leader, health switches to `SERVING`
- when the local node loses leadership, health switches to `NOT_SERVING`
- active worker sessions are closed so workers reconnect to the new leader

This is an important part of correctness. It prevents a demoted node from continuing to coordinate workers after its lease has ended.

### Known semantic limitation

Membership delete watches are currently surfaced as `EvtMemberLeft`. The code reserves `EvtMemberExpired`, but the current watch path cannot yet reliably distinguish an explicit removal from lease expiration on delete events.

Callers should therefore interpret `EvtMemberLeft` as "membership disappeared" rather than as a strictly graceful departure signal.

## Error handling

### Startup failures

`NewLeaderElection` returns an error if it cannot construct the etcd client. `Start` returns an error if it cannot create the session or register membership.

Startup should treat these as fatal for the node. `cmd/main.go` already does this and tears down partially started services in reverse order.

### Campaign failures

Failures from `Election.Campaign` are treated as transient unless the parent context has been cancelled. The loop logs the error, waits, and retries.

Recommended handling:

- keep the process alive
- rely on the retry loop
- surface the degraded state through logs and health checks

### Session loss

If `session.Done()` fires while the node is leader, the term ends with `EvtLeaderExpired`.

This case should be treated as stronger than a normal resignation:

- invalidate any cached assumption that this node can issue leader commands
- stop serving leader-only RPC traffic
- close worker coordination streams
- force reconnect and revalidation against the current leader

The gRPC server already closes sessions and flips health state when this happens.

### Graceful shutdown

On shutdown, `campaign` checks whether the local node is leader, tries to resign, and emits `EvtLeaderResigned`. `Close` then closes the shared session, which revokes the lease and removes both the election key and the membership key.

Even if explicit resign fails, closing the session still gives a safe cleanup path.

### Membership event decode failures

`observeMembers` can receive malformed data. `buildMembershipEvent` returns an error if JSON decoding fails or the etcd event type is unsupported.

The current handling is:

- log the error
- skip the bad event
- continue watching

This is correct behavior for the watcher loop because a single bad payload should not stop cluster supervision.

### Slow listeners and backpressure

Listener delivery is intentionally lossy under pressure. If a listener's sink channel is full, the event is dropped and the election service continues.

This protects leadership handling from being blocked by arbitrary downstream work, but it changes the contract for listeners:

- listeners must tolerate missed notifications
- listeners should reconstruct state from etcd or higher-level reconciliation if exact delivery matters
- event handlers should be idempotent

If a consumer needs durable, lossless sequencing, it should not rely only on the in-process callback stream.

### RPC rejection semantics

`requireLeader` defines the public-facing error contract for leader-only RPCs:

- if there is no known leader yet: `codes.Unavailable`
- if another node is the leader: `codes.FailedPrecondition`

This distinction matters operationally:

- `Unavailable` means the caller should retry because the cluster may still be converging
- `FailedPrecondition` means the caller should redirect to the reported leader

For worker streams, leadership loss on an established stream also returns `FailedPrecondition`, which instructs the worker to reconnect to the current leader.

## What the tests verify

The current test suite already exercises the main guarantees:

- `TestSingleLeaseForElectionAndMembership`: membership key and election key use the same lease and are cleaned up together
- `TestSessionExpirationRemovesMember`: lease expiry removes both leadership and membership state
- `TestThreeNodeLeaderElection`: a three-node cluster converges to exactly one leader and the expected membership view
- `TestLoseLeadershipDoesNotDeadlock`: leadership-loss fan-out does not deadlock
- `TestCloseWithActiveWatcherReturns`: shutdown is not blocked by active watchers
- `TestHandleLeaderEventClosesActiveWorkerStreams`: gRPC worker streams are terminated when leadership is lost

These tests are the executable evidence for the design described above.

## Practical guidance for future changes

When modifying this subsystem, preserve these invariants:

1. Election ownership and membership presence must remain tied to the same lease.
2. All state-changing leader actions must carry or derive a fencing token.
3. Followers must learn leadership from etcd-observed state, not only local callbacks.
4. Leadership loss must immediately stop leader-only RPC behavior.
5. Watcher or listener failures must not block the election loop itself.

If a change breaks any of those rules, it needs a strong justification because it weakens either safety or operational recovery.
