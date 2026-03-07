package events

import (
	"context"
	"testing"

	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestRegisterHandlerRecordsWorkerAndReturnsAck(t *testing.T) {
	state := NewControlPlaneStateStore()
	handler := NewRegisterHandler("leader-a", state)

	resp, err := handler.OnEvent(context.Background(), &myproto.RegisterEvent{
		Worker: &myproto.Worker{
			NodeId: "worker-1",
			Addr:   "127.0.0.1:9000",
		},
	})
	if err != nil {
		t.Fatalf("OnEvent returned error: %v", err)
	}

	worker, ok := state.Worker("worker-1")
	if !ok {
		t.Fatal("expected worker registration to be recorded")
	}
	if worker.Addr != "127.0.0.1:9000" {
		t.Fatalf("expected worker addr to be preserved, got %q", worker.Addr)
	}

	ack := resp.GetCommonAck()
	if ack == nil {
		t.Fatal("expected common ack response")
	}
	if ack.ServerId != "leader-a" || ack.EventType != "registration_ack" {
		t.Fatalf("unexpected ack response: %+v", ack)
	}
	if got := ack.Payload.AsMap()["worker_id"]; got != "worker-1" {
		t.Fatalf("expected worker_id payload, got %v", got)
	}
}

func TestDataPullStateHandlerRecordsUpdateAndReturnsAck(t *testing.T) {
	state := NewControlPlaneStateStore()
	handler := NewDataPullStateHandler("leader-a", state)

	resp, err := handler.OnEvent(context.Background(), &myproto.DataPullStatusUpdateEvent{
		DatasetId: "dataset-1",
		EpochId:   "epoch-1",
		Status:    myproto.DataPullStatusUpdateEvent_COMPLETED,
	})
	if err != nil {
		t.Fatalf("OnEvent returned error: %v", err)
	}

	update, ok := state.PullStatus("dataset-1", "epoch-1")
	if !ok {
		t.Fatal("expected pull status update to be recorded")
	}
	if update.Status != myproto.DataPullStatusUpdateEvent_COMPLETED {
		t.Fatalf("expected status to be preserved, got %v", update.Status)
	}

	ack := resp.GetCommonAck()
	if ack == nil {
		t.Fatal("expected common ack response")
	}
	if ack.EventType != "data_pull_status_ack" {
		t.Fatalf("unexpected ack event type: %s", ack.EventType)
	}
	if got := ack.Payload.AsMap()["status"]; got != "completed" {
		t.Fatalf("expected lower-case status payload, got %v", got)
	}
}

func TestCommonAckHandlerRecordsAckAndReturnsReceipt(t *testing.T) {
	state := NewControlPlaneStateStore()
	handler := NewCommonAckHandler("leader-a", state)

	resp, err := handler.OnEvent(context.Background(), &myproto.CommonAckEvent{
		ServerId:  "worker-1",
		EventType: "heartbeat_ack",
		Payload:   mustStruct(t, map[string]interface{}{"ok": true}),
	})
	if err != nil {
		t.Fatalf("OnEvent returned error: %v", err)
	}

	ack, ok := state.CommonAck("worker-1", "heartbeat_ack")
	if !ok {
		t.Fatal("expected common ack to be recorded")
	}
	if ack.ServerId != "worker-1" {
		t.Fatalf("expected source server id to be preserved, got %q", ack.ServerId)
	}

	receipt := resp.GetCommonAck()
	if receipt == nil {
		t.Fatal("expected receipt response")
	}
	if receipt.EventType != "ack_received" {
		t.Fatalf("unexpected receipt event type: %s", receipt.EventType)
	}
	if got := receipt.Payload.AsMap()["source_server_id"]; got != "worker-1" {
		t.Fatalf("expected source_server_id payload, got %v", got)
	}
}

func mustStruct(t *testing.T, value map[string]interface{}) *structpb.Struct {
	t.Helper()

	result, err := structpb.NewStruct(value)
	if err != nil {
		t.Fatalf("NewStruct returned error: %v", err)
	}
	return result
}
