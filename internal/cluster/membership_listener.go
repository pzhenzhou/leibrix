package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/puzpuzpuz/xsync/v4"
)

const (
	maxElapsedTime = 500 * time.Millisecond
)

// MembershipListener implements the Listener interface. It receives push-based
// events from the election service and places them onto a thread-safe MPMC queue
// for pull-based consumption by the application layer.
type MembershipListener struct {
	eventQueue *xsync.MPMCQueue[Event]
}

// NewMembershipListener creates a new listener with an internal MPMC queue.
func NewMembershipListener(queueSize int) *MembershipListener {
	return &MembershipListener{
		eventQueue: xsync.NewMPMCQueue[Event](queueSize),
	}
}

// OnLeaderChange is called by the Election service. It translates the raw
// event into our canonical, type-safe Event wrapper and enqueues it asynchronously.
// This method is non-blocking and returns immediately.
func (ml *MembershipListener) OnLeaderChange(ev LeaderEvent) {
	wrappedEvent := Event{
		Type:      ev.Type,
		Leader:    &ev,
		Fencing:   ev.Epoch,
		Timestamp: time.Now().Unix(),
	}
	ml.tryAddEvent(wrappedEvent, string(ev.Type), ev.Member.Name)
}

// OnMembershipChange is called by the Election service. It translates the raw
// event into our canonical, type-safe Event wrapper and enqueues it asynchronously.
// This method is non-blocking and returns immediately.
func (ml *MembershipListener) OnMembershipChange(ev MembershipEvent) {
	wrappedEvent := Event{
		Type:      ev.Type,
		Member:    &ev,
		Timestamp: time.Now().Unix(),
	}
	ml.tryAddEvent(wrappedEvent, string(ev.Type), ev.Member.Name)
}

// Consume returns the event queue for application components to pull from.
// This allows multiple concurrent consumers to process events in a competing
// consumer pattern.
func (ml *MembershipListener) Consume() *xsync.MPMCQueue[Event] {
	return ml.eventQueue
}

// tryAddEvent asynchronously attempts to enqueue an event with exponential backoff.
// This method launches a goroutine and returns immediately (non-blocking).
// The goroutine will retry the enqueue operation with exponential backoff until
// it succeeds or MaxElapsedTime is reached.
func (ml *MembershipListener) tryAddEvent(evt Event, eventType string, memberName string) {
	go func() {
		operation := func() (struct{}, error) {
			if !ml.eventQueue.TryEnqueue(evt) {
				// Return a retryable error (not permanent) so backoff continues
				return struct{}{}, context.DeadlineExceeded
			}
			return struct{}{}, nil
		}

		expBackoff := backoff.NewExponentialBackOff()
		expBackoff.InitialInterval = 1 * time.Microsecond
		expBackoff.MaxInterval = 10 * time.Millisecond
		expBackoff.Multiplier = 2.0
		expBackoff.RandomizationFactor = 0.1
		expBackoff.Reset()

		ctx := context.Background()
		_, err := backoff.Retry(ctx, operation,
			backoff.WithBackOff(expBackoff),
			backoff.WithMaxElapsedTime(maxElapsedTime))

		if err != nil {
			logger.Error(err, fmt.Sprintf("tryAddEvent failure %s, %s, %s",
				eventType, memberName, maxElapsedTime))
		} else {
			logger.Info("enqueued event",
				"type", eventType, "member", memberName)
		}
	}()
}
