package events

import (
	"context"
	"fmt"
	"strings"

	"github.com/puzpuzpuz/xsync/v4"
	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/protobuf/proto"
)

type ControlPlaneStateStore struct {
	workers    *xsync.Map[string, *myproto.Worker]
	pullStates *xsync.Map[string, *myproto.DataPullStatusUpdateEvent]
	commonAcks *xsync.Map[string, *myproto.CommonAckEvent]
}

func NewControlPlaneStateStore() *ControlPlaneStateStore {
	return &ControlPlaneStateStore{
		workers:    xsync.NewMap[string, *myproto.Worker](),
		pullStates: xsync.NewMap[string, *myproto.DataPullStatusUpdateEvent](),
		commonAcks: xsync.NewMap[string, *myproto.CommonAckEvent](),
	}
}

func (s *ControlPlaneStateStore) UpsertWorker(worker *myproto.Worker) {
	if worker == nil {
		return
	}
	s.workers.Store(worker.NodeId, proto.Clone(worker).(*myproto.Worker))
}

func (s *ControlPlaneStateStore) Worker(workerID string) (*myproto.Worker, bool) {
	return s.workers.Load(workerID)
}

func (s *ControlPlaneStateStore) UpdatePullStatus(update *myproto.DataPullStatusUpdateEvent) {
	if update == nil {
		return
	}
	s.pullStates.Store(pullStatusKey(update.DatasetId, update.EpochId), proto.Clone(update).(*myproto.DataPullStatusUpdateEvent))
}

func (s *ControlPlaneStateStore) PullStatus(datasetID, epochID string) (*myproto.DataPullStatusUpdateEvent, bool) {
	return s.pullStates.Load(pullStatusKey(datasetID, epochID))
}

func (s *ControlPlaneStateStore) RecordCommonAck(ack *myproto.CommonAckEvent) {
	if ack == nil {
		return
	}
	s.commonAcks.Store(commonAckKey(ack.ServerId, ack.EventType), proto.Clone(ack).(*myproto.CommonAckEvent))
}

func (s *ControlPlaneStateStore) CommonAck(serverID, eventType string) (*myproto.CommonAckEvent, bool) {
	return s.commonAcks.Load(commonAckKey(serverID, eventType))
}

type RegisterHandler struct {
	serverID string
	state    *ControlPlaneStateStore
}

func NewRegisterHandler(serverID string, state *ControlPlaneStateStore) EventHandler {
	return &RegisterHandler{
		serverID: serverID,
		state:    state,
	}
}

func (h *RegisterHandler) OnEvent(_ context.Context, message proto.Message) (*myproto.EventStreamMessage, error) {
	registerEvent, ok := message.(*myproto.RegisterEvent)
	if !ok {
		return nil, fmt.Errorf("unexpected register payload type: %T", message)
	}
	if registerEvent.Worker == nil || registerEvent.Worker.NodeId == "" {
		return nil, fmt.Errorf("register event is missing worker identity")
	}

	h.state.UpsertWorker(registerEvent.Worker)
	return CreateCommonAckEvent(h.serverID, "registration_ack", map[string]interface{}{
		"accepted":  true,
		"worker_id": registerEvent.Worker.NodeId,
	}), nil
}

type DataPullStateHandler struct {
	serverID string
	state    *ControlPlaneStateStore
}

func NewDataPullStateHandler(serverID string, state *ControlPlaneStateStore) EventHandler {
	return &DataPullStateHandler{
		serverID: serverID,
		state:    state,
	}
}

func (h *DataPullStateHandler) OnEvent(_ context.Context, message proto.Message) (*myproto.EventStreamMessage, error) {
	updateEvent, ok := message.(*myproto.DataPullStatusUpdateEvent)
	if !ok {
		return nil, fmt.Errorf("unexpected data pull payload type: %T", message)
	}
	if updateEvent.DatasetId == "" || updateEvent.EpochId == "" {
		return nil, fmt.Errorf("data pull update requires dataset_id and epoch_id")
	}

	h.state.UpdatePullStatus(updateEvent)
	return CreateCommonAckEvent(h.serverID, "data_pull_status_ack", map[string]interface{}{
		"dataset_id": updateEvent.DatasetId,
		"epoch_id":   updateEvent.EpochId,
		"status":     strings.ToLower(updateEvent.Status.String()),
	}), nil
}

type CommonAckHandler struct {
	serverID string
	state    *ControlPlaneStateStore
}

func NewCommonAckHandler(serverID string, state *ControlPlaneStateStore) EventHandler {
	return &CommonAckHandler{
		serverID: serverID,
		state:    state,
	}
}

func (h *CommonAckHandler) OnEvent(_ context.Context, message proto.Message) (*myproto.EventStreamMessage, error) {
	ackEvent, ok := message.(*myproto.CommonAckEvent)
	if !ok {
		return nil, fmt.Errorf("unexpected common ack payload type: %T", message)
	}
	if ackEvent.ServerId == "" {
		return nil, fmt.Errorf("common ack is missing server_id")
	}

	h.state.RecordCommonAck(ackEvent)
	return CreateCommonAckEvent(h.serverID, "ack_received", map[string]interface{}{
		"source_server_id": ackEvent.ServerId,
		"event_type":       ackEvent.EventType,
		"recorded":         true,
	}), nil
}

func pullStatusKey(datasetID, epochID string) string {
	return datasetID + "/" + epochID
}

func commonAckKey(serverID, eventType string) string {
	return serverID + "/" + eventType
}
