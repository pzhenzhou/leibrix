package grpc

import (
	"context"
	"fmt"
	"io"

	"github.com/pzhenzhou/leibri.io/internal/api/grpc/events"
	"github.com/pzhenzhou/leibri.io/internal/conf"
	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	_ myproto.ControlPlaneServiceServer = (*ControlPlaneService)(nil)
)

type ControlPlaneService struct {
	dispatcher     *events.EventDispatcher
	sessionManager *events.SessionManager
	config         *conf.LeibrixConfig
	leadership     LeadershipProvider
}

func NewControlPlaneService(
	config *conf.LeibrixConfig,
	dispatcher *events.EventDispatcher,
	sessionManager *events.SessionManager,
	leadership LeadershipProvider,
) myproto.ControlPlaneServiceServer {
	events.RegisterAllEventHandlers(dispatcher, config)
	c := &ControlPlaneService{
		config:         config,
		dispatcher:     dispatcher,
		sessionManager: sessionManager,
		leadership:     leadership,
	}
	return c
}
func (c *ControlPlaneService) CoordinateWorker(stream grpc.BidiStreamingServer[myproto.EventStreamMessage, myproto.EventStreamMessage]) error {
	if err := requireLeader(c.leadership, c.config.Node.NodeName); err != nil {
		return err
	}
	clientIp := getClientIp(stream.Context())
	logger.Info("ControlPlaneService CoordinateWorker called", "clientIp", clientIp)

	// Create and start session
	session := events.NewSession(stream.Context(), c.config.Node.NodeName, stream)
	session.Start()

	// Track worker ID for session management
	var workerID string
	recvCh := c.receiveWorkerMessages(session, stream)
	defer func() {
		// Unregister session when stream closes
		if workerID != "" {
			c.sessionManager.Unregister(workerID)
		}
		session.Close()
		logger.Info("Session closed", "worker_id", workerID, "client_ip", clientIp)
	}()

	// Main receive loop
	for {
		var (
			msg *myproto.EventStreamMessage
			err error
		)
		select {
		case <-session.Done():
			logger.Info("Worker stream closed after leadership change",
				"worker_id", workerID,
				"client_ip", clientIp)
			return status.Error(codes.FailedPrecondition, "control plane leadership changed; reconnect to the leader")
		case recvResult, ok := <-recvCh:
			if !ok {
				return nil
			}
			msg, err = recvResult.msg, recvResult.err
		}
		if err != nil {
			if err == io.EOF {
				logger.Info("Worker stream closed by client", "worker_id", workerID, "client_ip", clientIp)
				return nil
			}
			logger.Error(err, "Error receiving message from worker",
				"worker_id", workerID, "client_ip", clientIp)
			return err
		}

		// Log received message
		logger.Info("Received message from worker",
			"worker_id", msg.WorkerId,
			"tenant_id", msg.TenantId,
			"event_id", msg.EventId)

		// Handle RegisterEvent specially to register the session
		if reg, ok := msg.Payload.(*myproto.EventStreamMessage_RegisterEvent); ok {
			workerID = reg.RegisterEvent.Worker.NodeId
			c.sessionManager.Register(workerID, session)
			logger.Info("Worker registered",
				"worker_id", workerID,
				"addr", reg.RegisterEvent.Worker.Addr)
		}

		// Handle event asynchronously to avoid blocking receives
		go c.handleEventAsync(stream.Context(), session, msg)
	}
}

type recvResult struct {
	msg *myproto.EventStreamMessage
	err error
}

func (c *ControlPlaneService) receiveWorkerMessages(
	session *events.Session,
	stream grpc.BidiStreamingServer[myproto.EventStreamMessage, myproto.EventStreamMessage],
) <-chan recvResult {
	recvCh := make(chan recvResult)
	go func() {
		defer close(recvCh)
		for {
			msg, err := stream.Recv()
			select {
			case recvCh <- recvResult{msg: msg, err: err}:
			case <-session.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return recvCh
}

func (c *ControlPlaneService) handleEventAsync(
	ctx context.Context,
	session *events.Session,
	msg *myproto.EventStreamMessage,
) {
	// Dispatch to appropriate handler based on oneof payload
	ackMsg, err := c.handleEvent(ctx, msg)
	if err != nil {
		logger.Error(err, "Error handling event",
			"event_id", msg.EventId,
			"worker_id", msg.WorkerId)
		// Send error ack
		ackMsg = events.CreateCommonAckEvent(
			session.ServerId,
			"error_ack",
			map[string]interface{}{
				"error":             err.Error(),
				"original_event_id": msg.EventId,
			},
		)
	}

	if sendErr := session.Send(ackMsg); sendErr != nil {
		logger.Error(sendErr, "Error sending ack to session",
			"serverId", session.ServerId,
			"event_id", msg.EventId)
	}
}

func (c *ControlPlaneService) handleEvent(ctx context.Context, reqMsg *myproto.EventStreamMessage) (*myproto.EventStreamMessage, error) {
	switch payload := reqMsg.Payload.(type) {
	case *myproto.EventStreamMessage_RegisterEvent:
		return c.dispatcher.Dispatch(ctx, events.EventTypeRegister, payload.RegisterEvent)

	case *myproto.EventStreamMessage_HeartbeatEvent:
		return c.dispatcher.Dispatch(ctx, events.EventTypeHeartbeat, payload.HeartbeatEvent)

	case *myproto.EventStreamMessage_DataPullStatusUpdate:
		return c.dispatcher.Dispatch(ctx, events.EventTypeDataPullState, payload.DataPullStatusUpdate)

	case *myproto.EventStreamMessage_DataAssignment:
		return c.dispatcher.Dispatch(ctx, events.EventTypeDataAssigment, payload.DataAssignment)

	case *myproto.EventStreamMessage_CommonAck:
		return c.dispatcher.Dispatch(ctx, events.EventTypeCommonAck, payload.CommonAck)

	default:
		return nil, fmt.Errorf("unknown event type in message %s", reqMsg.EventId)
	}
}
