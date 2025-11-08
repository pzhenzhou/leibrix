package events

import (
	"context"
	"time"

	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
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

func (h *HeartbeatHandler) OnEvent(_ context.Context, _ proto.Message) (*myproto.EventResponse, error) {
	s, _ := structpb.NewStruct(map[string]interface{}{
		"ack_timestamp": time.Now().UnixNano(),
		"server_id":     h.serverId,
	})
	return &myproto.EventResponse{
		ServerId: h.serverId,
		Payload:  s,
	}, nil
}
