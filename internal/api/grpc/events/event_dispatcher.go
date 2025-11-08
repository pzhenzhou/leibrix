package events

import (
	"context"
	"fmt"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/pzhenzhou/leibri.io/internal/conf"
	"github.com/pzhenzhou/leibri.io/pkg/common"
	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/protobuf/proto"
)

var (
	logger = common.InitLogger()
)

type EventType string

const (
	EventTypeHeartbeat     EventType = "HEARTBEAT"
	EventTypeRegister      EventType = "REGISTER"
	EventTypeDataPullState EventType = "DATA_PULL_STATE"
	EventTypeDataAssigment EventType = "DATA_ASSIGNMENT"
)

type EventHandler interface {
	OnEvent(context.Context, proto.Message) (*myproto.EventResponse, error)
}

type EventDispatcher struct {
	handlers *xsync.Map[EventType, EventHandler]
}

func NewEventDispatcher() *EventDispatcher {
	return &EventDispatcher{
		handlers: xsync.NewMap[EventType, EventHandler](),
	}
}

func (d *EventDispatcher) Register(eventType EventType, handler EventHandler) {
	d.handlers.LoadOrStore(eventType, handler)
}

func (d *EventDispatcher) Dispatch(ctx context.Context, eventType EventType, event proto.Message) (*myproto.EventResponse, error) {
	handler, ok := d.handlers.Load(eventType)
	if !ok {
		return nil, fmt.Errorf("no handler for event type: %s", eventType)
	}
	return handler.OnEvent(ctx, event)
}

func RegisterAllEventHandlers(dispatcher *EventDispatcher, config *conf.LeibrixConfig) {
	// This function can be used to register all event handlers at once.
	panic("not implemented")
}
