package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"sync/atomic"

	"github.com/pzhenzhou/leibri.io/internal/conf"
	"github.com/pzhenzhou/leibri.io/pkg/common"
	clientv3 "go.etcd.io/etcd/client/v3"
	concurrencyv3 "go.etcd.io/etcd/client/v3/concurrency"
)

const (
	electionKey       = "/leibri.io/cluster/leader-election"
	membersKey        = "/leibri.io/cluster/members/"
	sessionTTL        = 15
	leaderChangeRetry = 3 * time.Second
)

type MemberRole string

const (
	Leader    MemberRole = "leader"
	Follower  MemberRole = "follower"
	Learner   MemberRole = "learner"
	Candidate MemberRole = "candidate"
)

type MemberNode struct {
	Name          string
	AdvertiseAddr string
	ListenAddr    string
	Meta          map[string]string
	Role          MemberRole `json:"-"`
}

type sink struct {
	ch   chan any
	once sync.Once
}

var logger = common.InitLogger()

type LeibrixLeaderElection struct {
	config    *conf.MasterConfig
	client    *clientv3.Client
	session   *concurrencyv3.Session
	election  *concurrencyv3.Election
	listeners map[uint64]*sink

	myNode *MemberNode

	// memberLease removed - using session.Lease() for both election and membership
	// This ensures atomic lifecycle management and automatic cleanup

	// shutdown
	cancel context.CancelFunc
	wg     sync.WaitGroup

	lock           sync.Mutex
	nextListenerID uint64
	//logger         *zap.Logger
}

func NewLeaderElection(config *conf.MasterConfig) (*LeibrixLeaderElection, error) {

	innerLogger, initLoggerErr := common.BuildZapLogger()
	if initLoggerErr != nil {
		panic(initLoggerErr)
	}
	cli, err := clientv3.New(clientv3.Config{
		Endpoints: config.Endpoints,
		Logger:    innerLogger,
	})
	if err != nil {
		logger.Error(err, "Failed to create etcd client", "node", config.MasterNode.NodeName)
		panic(err)
	}
	node := &MemberNode{
		Name:          config.MasterNode.NodeName,
		Role:          Candidate, // Start as a candidate
		AdvertiseAddr: fmt.Sprintf("%s:%d", config.MasterNode.HostName, config.MasterNode.AdvertisePort),
		ListenAddr:    fmt.Sprintf("%s:%d", config.MasterNode.HostName, config.MasterNode.ListenPort),
	}

	return &LeibrixLeaderElection{
		config:    config,
		client:    cli,
		myNode:    node,
		listeners: make(map[uint64]*sink),
	}, nil
}

func (l *LeibrixLeaderElection) Start(ctx context.Context) error {
	logger.Info("starting leader election service", "node", l.myNode.Name)
	session, err := concurrencyv3.NewSession(l.client, concurrencyv3.WithTTL(sessionTTL))
	if err != nil {
		logger.Error(err, fmt.Sprintf("failed to create etcd session for leader election service: %s", l.myNode.Name))
		return err
	}
	l.session = session
	l.election = concurrencyv3.NewElection(session, electionKey)

	if registerErr := l.registerMember(ctx); registerErr != nil {
		logger.Error(registerErr, fmt.Sprintf("failed to register leader election service: %s", l.myNode.Name))
		return registerErr
	}

	parentCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel

	l.wg.Add(1)
	go l.campaign(parentCtx)

	l.wg.Add(1)
	go l.observeLeader(parentCtx)

	l.wg.Add(1)
	go l.observeMembers(parentCtx)

	logger.Info("leader election service started", "node", l.myNode.Name)
	return nil
}

func (l *LeibrixLeaderElection) Close() error {
	logger.Info("stopping leader election service", "node", l.myNode.Name)

	// Check if cancel was initialized before calling
	// cancel is only set in Start(), so Close() before Start() would cause panic
	if l.cancel != nil {
		l.cancel()
		l.wg.Wait()
	}

	// Session.Close() automatically revokes its lease, which cleans up:
	//   1. The election key (/leibri.io/cluster/leader-election)
	//   2. The member key (/leibri.io/cluster/members/{node_name})
	// Both keys were registered with the same session lease, ensuring atomic cleanup.
	if l.session != nil {
		if sessionCloseErr := l.session.Close(); sessionCloseErr != nil {
			logger.Error(sessionCloseErr, fmt.Sprintf("failed to close session for leader election service: %s", l.myNode.Name))
		} else {
			logger.Info("session closed, lease automatically revoked", "node", l.myNode.Name)
		}
	}

	logger.Info("leader election service stopped")
	return l.client.Close()
}

func (l *LeibrixLeaderElection) registerMember(ctx context.Context) error {
	logger.Info("registering cluster member with election session")

	// Marshal member information
	memberJSON, err := json.Marshal(l.myNode)
	if err != nil {
		logger.Error(err, fmt.Sprintf("failed to marshal leader election session for member %s", l.myNode.Name))
		return fmt.Errorf("failed to marshal member data: %w", err)
	}

	memberKey := membersKey + l.config.MasterNode.NodeName

	// CRITICAL: Use session's lease for atomic lifecycle management.
	// This ensures member registration and election share the same lease,
	// guaranteeing consistent expiration and automatic cleanup.
	// When the session expires or is closed:
	//   1. The election key is automatically removed
	//   2. The member key is automatically removed
	// This prevents split-brain scenarios where a node appears as a member
	// but has lost its leadership session.
	_, putErr := l.client.Put(ctx, memberKey, string(memberJSON),
		clientv3.WithLease(l.session.Lease()))
	if putErr != nil {
		logger.Error(putErr, fmt.Sprintf("failed to register member %s", l.myNode.Name))
		return fmt.Errorf("failed to register member: %w", putErr)
	}

	logger.Info("member registered successfully with session lease",
		"key", memberKey,
		"lease_id", fmt.Sprintf("%x", l.session.Lease()))

	return nil
}

// keepMemberLeaseAlive has been removed.
// The session's internal keepalive mechanism now handles both
// the election lease and member registration lease automatically.

func (l *LeibrixLeaderElection) observeMembers(ctx context.Context) {
	defer l.wg.Done()

	watchChan := l.client.Watch(ctx, membersKey, clientv3.WithPrefix())

	for resp := range watchChan {
		for _, event := range resp.Events {
			var eventType EventType
			var member MemberNode

			key := event.Kv.Key
			value := event.Kv.Value

			switch event.Type {
			case clientv3.EventTypePut:
				if event.IsCreate() {
					eventType = EvtMemberJoined
				} else {
					eventType = EvtMemberUpdated
				}
			case clientv3.EventTypeDelete:
				eventType = EvtMemberLeft
				// For deletes, the value is gone, so we must use the previous value.
				value = event.PrevKv.Value
				key = event.PrevKv.Key
			}

			if err := json.Unmarshal(value, &member); err != nil {
				logger.Error(err, "failed to unmarshal member data", "node", l.myNode.Name)
				continue
			}
			// Fallback to key if name is not in value
			if member.Name == "" {
				member.Name = string(key)
			}

			l.broadcastMembershipEvent(MembershipEvent{
				Type:   eventType,
				Member: &member,
			})
		}
	}
}

func (l *LeibrixLeaderElection) broadcastLeaderEvent(ev LeaderEvent) {
	l.lock.Lock()
	sinksToNotify := make([]*sink, 0, len(l.listeners))
	for _, s := range l.listeners {
		sinksToNotify = append(sinksToNotify, s)
	}
	l.lock.Unlock()

	logger.Info("broadcasting leader event", "type", string(ev.Type), "leader", ev.Member.Name)
	for _, s := range sinksToNotify {
		select {
		case s.ch <- ev:
		default:
			logger.Info("listener channel full, dropping leader event")
		}
	}
}

func (l *LeibrixLeaderElection) broadcastMembershipEvent(ev MembershipEvent) {
	l.lock.Lock()
	sinksToNotify := make([]*sink, 0, len(l.listeners))
	for _, s := range l.listeners {
		sinksToNotify = append(sinksToNotify, s)
	}
	l.lock.Unlock()

	logger.Info("broadcasting membership event", "type", string(ev.Type), "member", ev.Member.Name)
	for _, s := range sinksToNotify {
		select {
		case s.ch <- ev:
		default:
			logger.Info("listener channel full, dropping membership event")
		}
	}
}

// campaign actively competes to become the leader. Only the winning node's
// Campaign() call will unblock. The loser's call will block until the leader
// fails. This goroutine is responsible for executing leader-specific logic
// upon winning the election.
func (l *LeibrixLeaderElection) campaign(ctx context.Context) {
	defer l.wg.Done()
	for {
		select {
		case <-ctx.Done():
			l.resign(context.Background())
			return
		default:
		}

		logger.Info("campaigning for leadership")
		leaderCtx, cancel := context.WithCancel(ctx)
		err := l.election.Campaign(leaderCtx, l.myNode.Name)
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to campaign leadership for node %s", l.myNode.Name))
			cancel()
			time.Sleep(leaderChangeRetry)
			continue
		}

		// became the leader
		l.becomeLeader()

		logger.Info("successfully elected as leader")

		select {
		case <-l.session.Done():
			logger.Info("leader session expired", "node", l.myNode.Name)
			l.loseLeadership()
		case <-ctx.Done():
			logger.Info("context cancelled, resigning leadership", "node", l.myNode.Name)
		}
		cancel()
	}
}

// observeLeader passively watches for leadership changes. It does not compete
// in the election. Its purpose is to notify all nodes (both leader and followers)
// of who the current leader is, ensuring a consistent view of the cluster state.
// This is the primary mechanism for followers to learn about the current leader.
func (l *LeibrixLeaderElection) observeLeader(ctx context.Context) {
	defer l.wg.Done()

	ch := l.election.Observe(ctx)
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				logger.Info("observer channel closed, stopping leader observation")
				return
			}
			if len(resp.Kvs) > 0 {
				l.lock.Lock()
				leaderName := string(resp.Kvs[0].Value)
				if leaderName == l.config.MasterNode.NodeName {
					l.myNode.Role = Leader
				} else {
					l.myNode.Role = Follower
				}
				l.lock.Unlock()

				logger.Info("observed new leader", "leader", leaderName)

				ev := LeaderEvent{
					Type: EvtLeaderElected,
					Member: &MemberNode{
						Name: leaderName,
					},
					Epoch: FencingToken(resp.Kvs[0].CreateRevision),
				}
				l.broadcastLeaderEvent(ev)
			} else {
				logger.Info("observed no leader is present")
				// No leader is present, could broadcast a "no leader" event if needed
			}

		case <-ctx.Done():
			logger.Info("context cancelled, stopping leader observation")
			return
		}
	}
}

func (l *LeibrixLeaderElection) becomeLeader() {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.myNode.Role = Leader
}

func (l *LeibrixLeaderElection) loseLeadership() {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.myNode.Role = Candidate

	ev := LeaderEvent{
		Type: EvtLeaderResigned,
		Member: &MemberNode{
			Name: l.config.MasterNode.NodeName,
		},
	}
	l.broadcastLeaderEvent(ev)
}
func (l *LeibrixLeaderElection) resign(ctx context.Context) error {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.myNode.Role == Leader {
		return l.election.Resign(ctx)
	}
	return nil
}

func (l *LeibrixLeaderElection) Watch(listener Listener) (unwatch func()) {
	id := atomic.AddUint64(&l.nextListenerID, 1)
	s := &sink{ch: make(chan any, 1)} // Buffered channel of 1 to decouple broadcaster

	// register
	l.lock.Lock()
	l.listeners[id] = s
	l.lock.Unlock()

	// single goroutine per listener: invokes blocking callbacks serially
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for ev := range s.ch {
			switch v := ev.(type) {
			case LeaderEvent:
				listener.OnLeaderChange(v) // BLOCKING by design
			case MembershipEvent:
				listener.OnMembershipChange(v) // BLOCKING by design
			}
		}
	}()

	return func() {
		l.lock.Lock()
		if old, ok := l.listeners[id]; ok {
			delete(l.listeners, id)
			old.once.Do(func() { close(old.ch) })
		}
		l.lock.Unlock()
	}
}

func (l *LeibrixLeaderElection) Members() ([]*MemberNode, error) {
	resp, err := l.client.Get(context.Background(), membersKey, clientv3.WithPrefix())
	if err != nil {
		logger.Error(err, fmt.Sprintf("failed to get members"), "node", l.myNode.Name)
		return nil, err
	}

	members := make([]*MemberNode, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var member MemberNode
		if err := json.Unmarshal(kv.Value, &member); err != nil {
			logger.Error(err, fmt.Sprintf("failed to unmarshal member"), "node", l.myNode.Name)
			continue
		}
		members = append(members, &member)
	}
	return members, nil
}
