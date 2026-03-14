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
	leaderChangeRetry = 5 * time.Second
	listenerQueueSize = 16
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
	leaderCh chan LeaderEvent
	memberCh chan MembershipEvent
	done     chan struct{}
	once     sync.Once
}

var logger = common.InitLogger()

type LeibrixLeaderElection struct {
	config    *conf.LeibrixConfig
	client    *clientv3.Client
	session   *concurrencyv3.Session
	election  *concurrencyv3.Election
	listeners map[uint64]*sink

	myNode *MemberNode

	// memberLease removed - using session.Lease() for both election and membership
	// This ensures atomic lifecycle assignment and automatic cleanup

	// shutdown
	cancel     context.CancelFunc
	serviceWg  sync.WaitGroup
	listenerWg sync.WaitGroup

	lock           sync.Mutex
	nextListenerID uint64
	currentLeader  string
	//logger         *zap.Logger
}

func NewLeaderElection(config *conf.LeibrixConfig) (*LeibrixLeaderElection, error) {
	innerLogger, initLoggerErr := common.BuildZapLogger()
	if initLoggerErr != nil {
		return nil, fmt.Errorf("failed to initialize election logger: %w", initLoggerErr)
	}
	clientConfig := common.DefaultEtcdClientConfig(config.ClusterConfig.ListenClientUrls)
	clientConfig.Logger = innerLogger

	cli, err := clientv3.New(clientConfig)
	if err != nil {
		logger.Error(err, "Failed to create etcd client", "node", config.Node.NodeName)
		return nil, fmt.Errorf("failed to create etcd client: %w", err)
	}
	node := &MemberNode{
		Name:          config.Node.NodeName,
		Role:          Candidate, // Start as a candidate
		AdvertiseAddr: fmt.Sprintf("%s:%d", config.Node.HostName, config.Node.AdvertisePort),
		ListenAddr:    fmt.Sprintf("%s:%d", config.Node.HostName, config.Node.ListenPort),
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
	session, err := concurrencyv3.NewSession(l.client,
		concurrencyv3.WithTTL(sessionTTL),
		concurrencyv3.WithContext(ctx))
	if err != nil {
		logger.Error(err, fmt.Sprintf("failed to create etcd session for leader election service: %s", l.myNode.Name))
		return err
	}
	l.session = session
	l.election = concurrencyv3.NewElection(session, electionKey)

	parentCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	memberWatchChan := l.client.Watch(parentCtx, membersKey, clientv3.WithPrefix(), clientv3.WithPrevKV())

	if registerErr := l.registerMember(ctx); registerErr != nil {
		logger.Error(registerErr, fmt.Sprintf("failed to register leader election service: %s", l.myNode.Name))
		cancel()
		_ = session.Close()
		return registerErr
	}

	l.serviceWg.Add(1)
	go l.campaign(parentCtx)

	l.serviceWg.Add(1)
	go l.observeLeader(parentCtx)

	l.serviceWg.Add(1)
	go l.observeMembers(memberWatchChan)

	logger.Info("leader election service started", "node", l.myNode.Name)
	return nil
}

func (l *LeibrixLeaderElection) Close() error {
	logger.Info("stopping leader election service", "node", l.myNode.Name)

	if l.cancel != nil {
		l.cancel()
		l.serviceWg.Wait()
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

	l.closeAllListeners()
	l.listenerWg.Wait()

	logger.Info("leader election service stopped")
	if l.client != nil {
		return l.client.Close()
	}
	return nil
}

func (l *LeibrixLeaderElection) registerMember(ctx context.Context) error {
	logger.Info("registering cluster member with election session")

	// Marshal member information
	memberJSON, err := json.Marshal(l.myNode)
	if err != nil {
		logger.Error(err, fmt.Sprintf("failed to marshal leader election session for member %s", l.myNode.Name))
		return fmt.Errorf("failed to marshal member data: %w", err)
	}

	memberKey := membersKey + l.config.Node.NodeName

	// CRITICAL: Use session's lease for atomic lifecycle assignment.
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

func (l *LeibrixLeaderElection) observeMembers(watchChan clientv3.WatchChan) {
	defer l.serviceWg.Done()

	for resp := range watchChan {
		for _, event := range resp.Events {
			membershipEvent, err := buildMembershipEvent(event)
			if err != nil {
				logger.Error(err, "failed to decode membership event", "node", l.myNode.Name)
				continue
			}
			l.broadcastMembershipEvent(membershipEvent)
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
		if !s.enqueueLeader(ev) {
			logger.Info("listener closed before leader event delivery")
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
		if !s.enqueueMembership(ev) {
			logger.Info("listener channel full, dropping membership event")
		}
	}
}

// campaign actively competes to become the leader. Only the winning node's
// Campaign() call will unblock. The loser's call will block until the leader
// fails. This goroutine is also responsible for classifying how the local
// leadership term ends so consumers can distinguish graceful resignation from
// session expiration.
func (l *LeibrixLeaderElection) campaign(ctx context.Context) {
	defer l.serviceWg.Done()
	for {
		select {
		case <-ctx.Done():
			if l.IsLeader() {
				if err := l.resign(context.Background()); err != nil {
					logger.Error(err, "failed to resign leadership", "node", l.myNode.Name)
				}
				l.loseLeadership(EvtLeaderResigned)
			}
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
			l.loseLeadership(EvtLeaderExpired)
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
	defer l.serviceWg.Done()

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
				if leaderName == l.config.Node.NodeName {
					l.myNode.Role = Leader
				} else {
					l.myNode.Role = Follower
				}
				l.currentLeader = leaderName
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
	l.currentLeader = l.myNode.Name
}

func (l *LeibrixLeaderElection) loseLeadership(eventType EventType) {
	l.lock.Lock()
	l.myNode.Role = Candidate
	l.currentLeader = ""
	nodeName := l.config.Node.NodeName
	l.lock.Unlock()

	ev := LeaderEvent{
		Type: eventType,
		Member: &MemberNode{
			Name: nodeName,
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
	s := newSink()

	// register
	l.lock.Lock()
	l.listeners[id] = s
	l.lock.Unlock()

	// single goroutine per listener: invokes blocking callbacks serially
	l.listenerWg.Add(1)
	go func() {
		defer l.listenerWg.Done()
		for {
			select {
			case <-s.done:
				return
			case ev := <-s.leaderCh:
				listener.OnLeaderChange(ev)
				continue
			default:
			}

			select {
			case <-s.done:
				return
			case ev := <-s.leaderCh:
				listener.OnLeaderChange(ev) // BLOCKING by design
			case ev := <-s.memberCh:
				listener.OnMembershipChange(ev) // BLOCKING by design
			}
		}
	}()

	return func() {
		l.removeListener(id)
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

func (l *LeibrixLeaderElection) IsLeader() bool {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.myNode != nil && l.myNode.Role == Leader
}

func (l *LeibrixLeaderElection) LeaderName() string {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.currentLeader
}

func (l *LeibrixLeaderElection) closeAllListeners() {
	l.lock.Lock()
	listeners := make([]*sink, 0, len(l.listeners))
	for id, listener := range l.listeners {
		delete(l.listeners, id)
		listeners = append(listeners, listener)
	}
	l.lock.Unlock()

	for _, listener := range listeners {
		listener.close()
	}
}

func (l *LeibrixLeaderElection) removeListener(id uint64) {
	l.lock.Lock()
	listener, ok := l.listeners[id]
	if ok {
		delete(l.listeners, id)
	}
	l.lock.Unlock()

	if ok {
		listener.close()
	}
}

func newSink() *sink {
	return &sink{
		leaderCh: make(chan LeaderEvent, 1),
		memberCh: make(chan MembershipEvent, listenerQueueSize),
		done:     make(chan struct{}),
	}
}

func (s *sink) close() {
	s.once.Do(func() {
		close(s.done)
	})
}

func (s *sink) enqueueLeader(ev LeaderEvent) bool {
	select {
	case <-s.done:
		return false
	default:
	}

	select {
	case s.leaderCh <- ev:
		return true
	default:
	}

	select {
	case <-s.leaderCh:
	default:
	}

	select {
	case s.leaderCh <- ev:
		return true
	case <-s.done:
		return false
	default:
		return false
	}
}

func (s *sink) enqueueMembership(ev MembershipEvent) bool {
	select {
	case <-s.done:
		return false
	default:
	}

	select {
	case s.memberCh <- ev:
		return true
	case <-s.done:
		return false
	default:
		return false
	}
}

func buildMembershipEvent(event *clientv3.Event) (MembershipEvent, error) {
	member := &MemberNode{}
	eventType := EvtMemberUpdated
	key := []byte(nil)
	value := []byte(nil)

	switch event.Type {
	case clientv3.EventTypePut:
		if event.IsCreate() {
			eventType = EvtMemberJoined
		}
		key = event.Kv.Key
		value = event.Kv.Value
	case clientv3.EventTypeDelete:
		eventType = EvtMemberLeft
		if event.PrevKv != nil {
			key = event.PrevKv.Key
			value = event.PrevKv.Value
		} else if event.Kv != nil {
			key = event.Kv.Key
		}
	default:
		return MembershipEvent{}, fmt.Errorf("unsupported event type: %v", event.Type)
	}

	if len(value) > 0 {
		if err := json.Unmarshal(value, member); err != nil {
			return MembershipEvent{}, err
		}
	}

	if member.Name == "" {
		member.Name = memberNameFromKey(string(key))
	}

	return MembershipEvent{
		Type:   eventType,
		Member: member,
	}, nil
}

func memberNameFromKey(key string) string {
	if len(key) <= len(membersKey) {
		return key
	}
	return key[len(membersKey):]
}
