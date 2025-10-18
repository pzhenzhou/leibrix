package cluster

// FencingToken is an epoch (leader key CreateRevision) used to prevent split-brain scenarios.
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
	Epoch  FencingToken // Fencing token (monotonic per leadership)
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
	Type      EventType
	Leader    *LeaderEvent
	Member    *MembershipEvent
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
