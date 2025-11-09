package events

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/grpc"
)

const (
	SessionClosed            = int32(1)
	SessionOpen              = int32(0)
	DefaultSessionOutboxSize = 1024
	DefaultTimeout           = 5 * time.Second
)

type Session struct {
	ctx        context.Context
	cancel     context.CancelFunc
	ServerId   string
	outbox     chan *myproto.EventStreamMessage
	stream     grpc.BidiStreamingServer[myproto.EventStreamMessage, myproto.EventStreamMessage]
	writerDone chan struct{}
	closed     int32
}

func NewSession(ctx context.Context, sererId string,
	stream grpc.BidiStreamingServer[myproto.EventStreamMessage, myproto.EventStreamMessage]) *Session {
	sessionCtx, cancel := context.WithCancel(ctx)
	return &Session{
		ctx:        sessionCtx,
		cancel:     cancel,
		ServerId:   sererId,
		stream:     stream,
		writerDone: make(chan struct{}),
		outbox:     make(chan *myproto.EventStreamMessage, DefaultSessionOutboxSize),
		closed:     SessionOpen,
	}
}

func (s *Session) drainOutbox() {
	timeout := time.After(DefaultTimeout)
	for {
		select {
		case <-timeout:
			return
		case resp, ok := <-s.outbox:
			if !ok {
				return
			}
			_ = s.stream.Send(resp)
		default:
			return
		}
	}
}

// Send enqueues an event message to be sent to the client
func (s *Session) Send(msg *myproto.EventStreamMessage) error {
	select {
	case s.outbox <- msg:
		return nil
	case <-s.ctx.Done():
		return fmt.Errorf("session closed: %w", s.ctx.Err())
	}
}

func (s *Session) Start() {
	go s.writerLoop()
}

func (s *Session) writerLoop() {
	defer close(s.writerDone)
	for {
		select {
		case <-s.ctx.Done():
			s.drainOutbox()
			return
		case resp := <-s.outbox:
			if err := s.stream.Send(resp); err != nil {
				logger.Error(err, "send response error", "server_id", s.ServerId)
				s.Close()
				return
			}
		}
	}
}

func (s *Session) Close() {
	if atomic.CompareAndSwapInt32(&s.closed, SessionOpen, SessionClosed) {
		s.cancel()
		close(s.outbox)
		logger.Info("Session closed")
	} else {
		logger.Info("Session already closed", "server_id", s.ServerId)
	}
}
