package grpc

import (
	"testing"

	"github.com/pzhenzhou/leibri.io/internal/cluster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeLeadershipProvider struct {
	isLeader   bool
	leaderName string
}

func (f fakeLeadershipProvider) IsLeader() bool {
	return f.isLeader
}

func (f fakeLeadershipProvider) LeaderName() string {
	return f.leaderName
}

func (fakeLeadershipProvider) Watch(cluster.Listener) func() {
	return func() {}
}

func TestRequireLeader_AllowsLeader(t *testing.T) {
	if err := requireLeader(fakeLeadershipProvider{isLeader: true, leaderName: "node-a"}, "node-a"); err != nil {
		t.Fatalf("requireLeader returned error for leader: %v", err)
	}
}

func TestRequireLeader_RejectsFollowerWithKnownLeader(t *testing.T) {
	err := requireLeader(fakeLeadershipProvider{isLeader: false, leaderName: "node-b"}, "node-a")
	if err == nil {
		t.Fatal("expected follower rejection error")
	}

	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected %s, got %s", codes.FailedPrecondition, status.Code(err))
	}
}

func TestRequireLeader_RejectsFollowerWithoutLeader(t *testing.T) {
	err := requireLeader(fakeLeadershipProvider{isLeader: false}, "node-a")
	if err == nil {
		t.Fatal("expected leader-not-ready error")
	}

	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected %s, got %s", codes.Unavailable, status.Code(err))
	}
}
