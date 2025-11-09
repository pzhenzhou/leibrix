package events

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// SessionManager manages all active worker sessions
type SessionManager struct {
	sessions   *xsync.Map[string, *Session] // workerID -> Session
	sessionCnt atomic.Int64
}

// NewSessionManager creates a new session manager
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: xsync.NewMap[string, *Session](),
	}
}

// Register adds a session to the manager, indexed by worker ID
// If a session already exists for this worker, it closes the old one first (reconnection scenario)
func (sm *SessionManager) Register(workerID string, session *Session) {
	// Check if worker already has a session (reconnection case)
	if existingSession, loaded := sm.sessions.LoadAndStore(workerID, session); loaded {
		logger.Info("Worker reconnected, closing old session",
			"worker_id", workerID,
			"server_id", existingSession.ServerId)
		existingSession.Close()
	} else {
		sm.sessionCnt.Add(1)
	}

	logger.Info("Session registered",
		"worker_id", workerID,
		"server_id", session.ServerId,
		"total_sessions", sm.sessionCnt.Load())
}

// Unregister removes a session from the manager
func (sm *SessionManager) Unregister(workerID string) {
	if session, loaded := sm.sessions.LoadAndDelete(workerID); loaded {
		sm.sessionCnt.Add(-1)
		logger.Info("Session unregistered",
			"worker_id", workerID,
			"server_id", session.ServerId,
			"total_sessions", sm.sessionCnt.Load())
	}
}

// Get retrieves a session by worker ID
func (sm *SessionManager) Get(workerID string) (*Session, bool) {
	return sm.sessions.Load(workerID)
}

// ToWorker sends an event message to a specific worker
func (sm *SessionManager) ToWorker(workerID string, msg *myproto.EventStreamMessage) error {
	session, ok := sm.sessions.Load(workerID)
	if !ok {
		return fmt.Errorf("worker %s not connected", workerID)
	}
	return session.Send(msg)
}

// Broadcast sends an event message to all connected workers
func (sm *SessionManager) Broadcast(msg *myproto.EventStreamMessage) {
	var successCount, failCount int

	sm.sessions.Range(func(workerID string, session *Session) bool {
		if err := session.Send(msg); err != nil {
			logger.Error(err, "Failed to broadcast to worker", "worker_id", workerID)
			failCount++
		} else {
			successCount++
		}
		return true // continue iteration
	})

	logger.Info("Broadcast completed",
		"success_count", successCount,
		"fail_count", failCount,
		"total_sessions", sm.sessionCnt.Load())
}

// SendCommonAck sends a common acknowledgment event to a specific worker
func (sm *SessionManager) SendCommonAck(workerID string, serverID string, eventType string, payload map[string]interface{}) error {
	msg := CreateCommonAckEvent(serverID, eventType, payload)
	msg.WorkerId = workerID
	return sm.ToWorker(workerID, msg)
}

// SendDataAssignment sends a data assignment event to a specific worker
func (sm *SessionManager) SendDataAssignment(workerID string, serverID string, assignment *myproto.DataAssignmentEvent) error {
	msg := &myproto.EventStreamMessage{
		EventId:  generateEventID(),
		WorkerId: workerID,
		Payload: &myproto.EventStreamMessage_DataAssignment{
			DataAssignment: assignment,
		},
	}
	return sm.ToWorker(workerID, msg)
}

// BroadcastCommonAck broadcasts a common acknowledgment event to all connected workers
func (sm *SessionManager) BroadcastCommonAck(serverID string, eventType string, payload map[string]interface{}) {
	msg := CreateCommonAckEvent(serverID, eventType, payload)
	sm.Broadcast(msg)
}

// ActiveWorkerCount returns the number of connected workers

// Helper functions for creating EventStreamMessage instances

// CreateCommonAckEvent creates a CommonAckEvent wrapped in EventStreamMessage
func CreateCommonAckEvent(serverID string, eventType string, payload map[string]interface{}) *myproto.EventStreamMessage {
	return &myproto.EventStreamMessage{
		EventId: generateEventID(),
		Payload: &myproto.EventStreamMessage_CommonAck{
			CommonAck: &myproto.CommonAckEvent{
				ServerId:  serverID,
				EventType: eventType,
				Payload:   mapToStruct(payload),
			},
		},
	}
}

// generateEventID generates a unique event ID with timestamp
func generateEventID() string {
	return fmt.Sprintf("evt_%d", time.Now().UnixNano())
}

// mapToStruct converts a map to google.protobuf.Struct
func mapToStruct(m map[string]interface{}) *structpb.Struct {
	if m == nil {
		return nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		logger.Error(err, "Failed to convert map to Struct")
		return nil
	}
	return s
}
func (sm *SessionManager) ActiveWorkerCount() int64 {
	return sm.sessionCnt.Load()
}

// ListWorkers returns a list of all connected worker IDs
func (sm *SessionManager) ListWorkers() []string {
	workers := make([]string, 0, sm.sessionCnt.Load())
	sm.sessions.Range(func(workerID string, _ *Session) bool {
		workers = append(workers, workerID)
		return true
	})
	return workers
}

// Close gracefully closes all sessions
func (sm *SessionManager) Close() {
	logger.Info("Closing all sessions", "total", sm.sessionCnt.Load())

	sm.sessions.Range(func(workerID string, session *Session) bool {
		session.Close()
		return true
	})

	sm.sessions.Clear()
	sm.sessionCnt.Store(0)
}
