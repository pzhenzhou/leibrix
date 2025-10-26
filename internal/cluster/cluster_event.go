package cluster

// FencingToken is a monotonically increasing epoch number used to prevent split-brain scenarios
// in distributed systems. It acts as a logical clock for leadership changes.
//
// Problem: Split-Brain Scenario
// In a distributed system, network partitions can cause multiple nodes to believe they are
// the leader simultaneously. Without coordination, both nodes could issue conflicting commands,
// leading to data corruption or inconsistent state.
//
// Solution: Fencing with Monotonic Tokens
// Each leadership change is assigned a unique, monotonically increasing token (epoch).
// Components receiving commands MUST reject operations from stale epochs (lower tokens).
//
// Implementation in Leibrix:
//   - When a leader is elected via etcd, the election key receives a CreateRevision from etcd.
//   - This CreateRevision is a cluster-wide monotonic counter that serves as the fencing token.
//   - The token is embedded in LeaderEvent and propagated to all consumers (workers, gateways).
//   - Workers and gateways compare incoming command epochs against their known current epoch.
//   - Commands with epoch < current_epoch are REJECTED as stale (from an old leader).
//
// Example Timeline:
//  1. Node A elected as leader    → epoch = 100 (from etcd CreateRevision)
//  2. Network partition occurs
//  3. Node A still thinks it's leader (epoch 100)
//  4. Node B elected as new leader → epoch = 101 (higher revision)
//  5. Network heals
//  6. Node A sends command (epoch 100) → REJECTED by workers (stale)
//  7. Node B sends command (epoch 101) → ACCEPTED by workers (current)
//
// This ensures that even if an old leader recovers from a partition, its commands
// cannot corrupt the system state managed by the new leader.
type FencingToken uint64

// EventType enumerates all possible cluster events for the unified event bus.
type EventType string

const (
	// Leader Events
	EvtLeaderElected  EventType = "leader-elected"
	EvtLeaderResigned EventType = "leader-resigned"
	EvtLeaderExpired  EventType = "leader-expired"

	// Membership Events
	EvtMemberJoined  EventType = "member-joined"
	EvtMemberLeft    EventType = "member-left"
	EvtMemberExpired EventType = "member-expired"
	EvtMemberUpdated EventType = "member-updated"
)

// LeaderEvent is the payload for leader-related events.
type LeaderEvent struct {
	Type   EventType
	Member *MemberNode
	// Epoch is the fencing token for this leadership term. It's derived from the etcd
	// election key's CreateRevision, which is a monotonically increasing cluster-wide counter.
	// Consumers (workers, gateways) MUST check this epoch and reject commands from older epochs
	// to prevent split-brain scenarios where an old leader's stale commands could corrupt state.
	Epoch FencingToken
}

// MembershipEvent is the payload for membership-related events.
type MembershipEvent struct {
	Type   EventType
	Member *MemberNode
}

// Event is a type-safe container for all cluster events.
// It is the message that will be placed on the MPMC queue.
// For any given event, only one of the payload fields should be set.
type Event struct {
	Type   EventType
	Leader *LeaderEvent
	Member *MembershipEvent
	// Fencing is the current epoch/fencing token. For LeaderEvent, this is populated
	// from Leader.Epoch. For MembershipEvent, this field may be 0 (membership changes
	// don't require fencing as they don't issue state-changing commands).
	Fencing   FencingToken
	Timestamp int64
}

// Listener is the interface that the MembershipListener will implement.
// The existing Election service will push events to this interface.
type Listener interface {
	// OnLeaderChange is called when a leader is elected, resigns, or expires.
	OnLeaderChange(ev LeaderEvent)
	// OnMembershipChange is called when a member joins, leaves, is updated, or expires.
	OnMembershipChange(ev MembershipEvent)
}
