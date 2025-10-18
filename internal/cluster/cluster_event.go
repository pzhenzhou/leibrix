package cluster

type FencingToken uint64 // epoch (leader key CreateRevision)

type LeaderEventType string
type MembershipEventType string

const (
	LeaderElected  LeaderEventType = "elected"
	LeaderResigned LeaderEventType = "resigned"
	LeaderExpired  LeaderEventType = "expired"
)

const (
	MemberJoined  MembershipEventType = "joined"
	MemberLeft    MembershipEventType = "left"
	MemberExpired MembershipEventType = "expired"
	MemberUpdated MembershipEventType = "updated"
)

type LeaderEvent struct {
	Type   LeaderEventType
	Member *MemberNode
	Epoch  FencingToken // fencing token (monotonic per leadership)
}

type MembershipEvent struct {
	Type   MembershipEventType
	Member *MemberNode
}
