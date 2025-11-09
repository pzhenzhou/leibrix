package events

import (
	"context"
	"time"

	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/protobuf/proto"
)

var _ EventHandler = (*HeartbeatHandler)(nil)

type HeartbeatHandler struct {
	serverId string
}

func NewHeartbeatHandler(serverId string) EventHandler {
	return &HeartbeatHandler{
		serverId: serverId,
	}
}

func (h *HeartbeatHandler) OnEvent(_ context.Context, _ proto.Message) (*myproto.EventStreamMessage, error) {
	// Create acknowledgment with heartbeat response
	payload := map[string]interface{}{
		"ack_timestamp": time.Now().UnixNano(),
		"server_id":     h.serverId,
		"status":        "healthy",
	}

	// Return CommonAckEvent wrapped in EventStreamMessage
	return CreateCommonAckEvent(h.serverId, "heartbeat_ack", payload), nil
}
