package grpc

import (
	"fmt"

	"github.com/pzhenzhou/leibri.io/internal/cluster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type LeadershipProvider interface {
	IsLeader() bool
	LeaderName() string
	Watch(listener cluster.Listener) (unwatch func())
}

func requireLeader(leadership LeadershipProvider, nodeName string) error {
	if leadership == nil || leadership.IsLeader() {
		return nil
	}

	leaderName := leadership.LeaderName()
	if leaderName == "" {
		return status.Error(codes.Unavailable, "control plane leader is not ready yet")
	}

	return status.Error(codes.FailedPrecondition,
		fmt.Sprintf("node %s is not the control plane leader; current leader is %s", nodeName, leaderName))
}
