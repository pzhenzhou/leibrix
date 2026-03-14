package grpc

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/pzhenzhou/leibri.io/internal/api/grpc/events"
	"github.com/pzhenzhou/leibri.io/internal/cluster"
	"github.com/pzhenzhou/leibri.io/internal/conf"
	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestHandleLeaderEventClosesActiveWorkerStreams(t *testing.T) {
	config := &conf.LeibrixConfig{
		Node: conf.NodeConfig{
			NodeName: "node-a",
		},
	}
	sessionManager := events.NewSessionManager()
	dispatcher := events.NewEventDispatcher()
	service := NewControlPlaneService(config, dispatcher, sessionManager, fakeLeadershipProvider{
		isLeader:   true,
		leaderName: "node-a",
	}).(*ControlPlaneService)

	server := &LeibrixGRPCServer{
		config:         config,
		healthServer:   health.NewServer(),
		sessionManager: sessionManager,
	}
	server.setServingState(true)

	stream := newFakeCoordinateWorkerStream()
	errCh := make(chan error, 1)
	go func() {
		errCh <- service.CoordinateWorker(stream)
	}()

	stream.push(&myproto.EventStreamMessage{
		EventId:  "evt-register",
		WorkerId: "worker-1",
		Payload: &myproto.EventStreamMessage_RegisterEvent{
			RegisterEvent: &myproto.RegisterEvent{
				Worker: &myproto.Worker{
					NodeId: "worker-1",
					Addr:   "127.0.0.1:9000",
				},
			},
		},
	})

	waitFor(t, 2*time.Second, func() bool {
		return sessionManager.ActiveWorkerCount() == 1 && stream.sendCount() >= 1
	})

	server.handleLeaderEvent(cluster.LeaderEvent{
		Type:   cluster.EvtLeaderExpired,
		Member: &cluster.MemberNode{Name: "node-a"},
	})
	stream.closeRecv()

	select {
	case err := <-errCh:
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("expected leadership-loss error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CoordinateWorker did not exit after leadership loss")
	}

	if sessionManager.ActiveWorkerCount() != 0 {
		t.Fatalf("expected all sessions to be closed, got %d", sessionManager.ActiveWorkerCount())
	}

	resp, err := server.healthServer.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{
		Service: "leibrix.ControlPlaneService",
	})
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("expected NOT_SERVING after leadership loss, got %s", resp.Status)
	}
}

type fakeCoordinateWorkerStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	recvCh chan *myproto.EventStreamMessage

	mu   sync.Mutex
	sent []*myproto.EventStreamMessage
}

func newFakeCoordinateWorkerStream() *fakeCoordinateWorkerStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeCoordinateWorkerStream{
		ctx:    ctx,
		cancel: cancel,
		recvCh: make(chan *myproto.EventStreamMessage, 4),
	}
}

func (s *fakeCoordinateWorkerStream) push(msg *myproto.EventStreamMessage) {
	s.recvCh <- msg
}

func (s *fakeCoordinateWorkerStream) closeRecv() {
	select {
	case <-s.ctx.Done():
	default:
		close(s.recvCh)
		s.cancel()
	}
}

func (s *fakeCoordinateWorkerStream) sendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *fakeCoordinateWorkerStream) SetHeader(metadata.MD) error { return nil }

func (s *fakeCoordinateWorkerStream) SendHeader(metadata.MD) error { return nil }

func (s *fakeCoordinateWorkerStream) SetTrailer(metadata.MD) {}

func (s *fakeCoordinateWorkerStream) Context() context.Context { return s.ctx }

func (s *fakeCoordinateWorkerStream) Send(msg *myproto.EventStreamMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return nil
}

func (s *fakeCoordinateWorkerStream) Recv() (*myproto.EventStreamMessage, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case msg, ok := <-s.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	}
}

func (s *fakeCoordinateWorkerStream) SendMsg(any) error { return nil }

func (s *fakeCoordinateWorkerStream) RecvMsg(any) error { return nil }

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
