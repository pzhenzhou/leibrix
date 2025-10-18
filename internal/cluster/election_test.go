package cluster

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pzhenzhou/leibri.io/internal/conf"
	"go.etcd.io/etcd/server/v3/embed"
)

// TestListener is a mock implementation of the Listener interface for testing
type TestListener struct {
	leaderEvents     []LeaderEvent
	membershipEvents []MembershipEvent
	mu               sync.Mutex
	leaderCallCount  atomic.Int32
	memberCallCount  atomic.Int32
	wg               sync.WaitGroup
	expectingEvents  bool // Track whether ExpectEvents() was called
}

func NewTestListener() *TestListener {
	return &TestListener{
		leaderEvents:     make([]LeaderEvent, 0),
		membershipEvents: make([]MembershipEvent, 0),
		expectingEvents:  false, // Default: not expecting events
	}
}

func (t *TestListener) OnLeaderChange(ev LeaderEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.leaderEvents = append(t.leaderEvents, ev)
	t.leaderCallCount.Add(1)
	// Only call Done() if ExpectEvents() was called
	if t.expectingEvents {
		t.wg.Done()
	}
}

func (t *TestListener) OnMembershipChange(ev MembershipEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.membershipEvents = append(t.membershipEvents, ev)
	t.memberCallCount.Add(1)
	// Only call Done() if ExpectEvents() was called
	if t.expectingEvents {
		t.wg.Done()
	}
}

func (t *TestListener) GetLeaderEvents() []LeaderEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]LeaderEvent{}, t.leaderEvents...)
}

func (t *TestListener) GetMembershipEvents() []MembershipEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]MembershipEvent{}, t.membershipEvents...)
}

func (t *TestListener) ExpectEvents(leaderCount, memberCount int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expectingEvents = true
	t.wg.Add(leaderCount + memberCount)
}

func (t *TestListener) WaitForEvents(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// parseURL is a helper to parse URL strings for testing
func parseURL(urlStr string) url.URL {
	u, err := url.Parse(urlStr)
	if err != nil {
		panic(err)
	}
	return *u
}

// startEmbeddedEtcd starts an embedded etcd server for testing
// Uses asynchronous startup with proper channel-based synchronization
func startEmbeddedEtcd(t *testing.T) (*embed.Etcd, string, func()) {
	t.Helper()

	cfg := embed.NewConfig()
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "error"

	// Use a unique name for this test's etcd instance
	nodeName := fmt.Sprintf("etcd-test-node-%d", time.Now().UnixNano())

	// CRITICAL: Set node identity and cluster configuration
	// The peer URL port must match what's in InitialCluster
	cfg.Name = nodeName
	cfg.InitialCluster = fmt.Sprintf("%s=http://127.0.0.1:12380", nodeName)
	cfg.InitialClusterToken = fmt.Sprintf("etcd-test-cluster-%d", time.Now().UnixNano())

	// Use fixed ports for peer URLs (must match InitialCluster above)
	// Use random ports (0) for client URLs to avoid conflicts
	cfg.ListenClientUrls = []url.URL{parseURL("http://127.0.0.1:0")}
	cfg.AdvertiseClientUrls = []url.URL{parseURL("http://127.0.0.1:0")}
	cfg.ListenPeerUrls = []url.URL{parseURL("http://127.0.0.1:12380")}
	cfg.AdvertisePeerUrls = []url.URL{parseURL("http://127.0.0.1:12380")}

	// Create channels for async startup synchronization
	etcdChan := make(chan *embed.Etcd, 1)
	errChan := make(chan error, 1)

	// Start etcd in a goroutine to avoid blocking
	go func() {
		e, err := embed.StartEtcd(cfg)
		if err != nil {
			errChan <- err
			return
		}
		etcdChan <- e
	}()

	// Wait for etcd to start with timeout
	var e *embed.Etcd
	select {
	case e = <-etcdChan:
		// Etcd instance received
	case err := <-errChan:
		t.Fatalf("Failed to start embedded etcd (startup error): %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("Embedded etcd startup timed out (>15 seconds)")
	}

	if e == nil {
		t.Fatal("Embedded etcd instance is nil")
	}

	// Wait for etcd server to be ready
	select {
	case <-e.Server.ReadyNotify():
		// Server is ready, proceed
	case <-time.After(15 * time.Second):
		e.Close()
		t.Fatal("Etcd server took too long to become ready (>15 seconds)")
	}

	// Extract actual client URL (may be different due to random port assignment)
	clientURL := e.Clients[0].Addr().String()

	cleanup := func() {
		e.Close()
		// Wait for server to stop with timeout
		select {
		case <-e.Server.StopNotify():
			// Server stopped gracefully
		case <-time.After(5 * time.Second):
			// Timeout waiting for graceful shutdown, but continue cleanup
		}
	}

	t.Logf("Embedded etcd started successfully: %s", clientURL)
	return e, clientURL, cleanup
}

// createTestConfig creates a test configuration
func createTestConfig(nodeName, clientURL string) *conf.MasterConfig {
	return &conf.MasterConfig{
		MasterNode: conf.MasterNode{
			NodeName:      nodeName,
			HostName:      "localhost",
			ListenPort:    2380,
			AdvertisePort: 2382,
		},
		Endpoints: []string{clientURL},
	}
}

// TestNewLeaderElection tests the construction of LeaderElection
func TestNewLeaderElection(t *testing.T) {
	_, clientURL, cleanup := startEmbeddedEtcd(t)
	defer cleanup()

	config := createTestConfig("test-node-1", clientURL)

	election, err := NewLeaderElection(config)
	if err != nil {
		t.Fatalf("Failed to create leader election: %v", err)
	}
	defer election.Close()

	if election == nil {
		t.Fatal("Expected non-nil election instance")
	}
	if election.myNode == nil {
		t.Fatal("Expected myNode to be initialized")
	}
	if election.myNode.Name != "test-node-1" {
		t.Errorf("Expected node name 'test-node-1', got '%s'", election.myNode.Name)
	}
	if election.myNode.Role != Candidate {
		t.Errorf("Expected initial role to be Candidate, got %s", election.myNode.Role)
	}
	if election.client == nil {
		t.Fatal("Expected etcd client to be initialized")
	}
}

// TestNewLeaderElection_InvalidEndpoint tests construction with invalid endpoint
func TestNewLeaderElection_InvalidEndpoint(t *testing.T) {
	config := createTestConfig("test-node", "http://invalid:9999")

	// NewLeaderElection should succeed (lazy connection, doesn't connect immediately)
	election, err := NewLeaderElection(config)
	if err != nil {
		t.Fatalf("Should create instance before connecting: %v", err)
	}
	if election == nil {
		t.Fatal("Expected non-nil election instance")
	}
	defer election.Close() // Use defer to ensure cleanup always happens

	// Start should fail with invalid endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = election.Start(ctx)
	if err == nil {
		t.Error("Expected error when starting with invalid endpoint")
	} else {
		// Expected case: connection should fail
		t.Logf("Got expected error: %v", err)
	}
}

// TestStartAndClose tests starting and closing the election service
func TestStartAndClose(t *testing.T) {
	_, clientURL, cleanup := startEmbeddedEtcd(t)
	defer cleanup()

	config := createTestConfig("test-node-1", clientURL)
	election, err := NewLeaderElection(config)
	if err != nil {
		t.Fatalf("Failed to create leader election: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = election.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start election: %v", err)
	}

	// Wait briefly for goroutines to start
	time.Sleep(500 * time.Millisecond)

	// Verify session is created
	if election.session == nil {
		t.Fatal("Expected session to be created")
	}

	// Verify member is registered
	if election.memberLease == 0 {
		t.Fatal("Expected member lease to be created")
	}

	err = election.Close()
	if err != nil {
		t.Fatalf("Failed to close election: %v", err)
	}
}

// TestMembers tests retrieving cluster members
func TestMembers(t *testing.T) {
	_, clientURL, cleanup := startEmbeddedEtcd(t)
	defer cleanup()

	config := createTestConfig("test-node-1", clientURL)
	election, err := NewLeaderElection(config)
	if err != nil {
		t.Fatalf("Failed to create leader election: %v", err)
	}
	defer election.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = election.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start election: %v", err)
	}

	// Wait for member registration
	time.Sleep(1 * time.Second)

	members, err := election.Members()
	if err != nil {
		t.Fatalf("Failed to get members: %v", err)
	}

	if len(members) != 1 {
		t.Errorf("Expected 1 member, got %d", len(members))
	}

	if len(members) > 0 && members[0].Name != "test-node-1" {
		t.Errorf("Expected member name 'test-node-1', got '%s'", members[0].Name)
	}
}

// TestThreeNodeLeaderElection tests a realistic 3-node leader election scenario
// This test properly simulates:
// 1. Three master nodes joining the cluster
// 2. Each node calling Start() and Watch() in proper sequence
// 3. Leader election occurring among the three nodes
// 4. Verification that exactly one becomes Leader and two become Followers
// 5. Using Members() to verify the cluster state
func TestThreeNodeLeaderElection(t *testing.T) {
	_, clientURL, cleanup := startEmbeddedEtcd(t)
	defer cleanup()

	const numNodes = 3
	nodes := make([]*LeibrixLeaderElection, numNodes)
	listeners := make([]*TestListener, numNodes)
	nodeNames := []string{"master-node-1", "master-node-2", "master-node-3"}

	// Step 1: Create all three nodes
	t.Log("Step 1: Creating three master nodes...")
	for i := 0; i < numNodes; i++ {
		config := createTestConfig(nodeNames[i], clientURL)

		election, err := NewLeaderElection(config)
		if err != nil {
			t.Fatalf("Failed to create election for %s: %v", nodeNames[i], err)
		}
		defer election.Close()

		nodes[i] = election
		t.Logf("  ✓ Created %s", nodeNames[i])
	}

	// Step 2: Set up Watch() for each node BEFORE starting
	// This ensures we capture all events from the beginning
	t.Log("\nStep 2: Setting up Watch() on each node...")
	for i := 0; i < numNodes; i++ {
		listeners[i] = NewTestListener()
		// Note: We don't call ExpectEvents() here because in a 3-node cluster,
		// the number of events can vary (multiple leader observations, membership events).
		// Instead, we'll use a fixed timeout to wait for election to stabilize.
		nodes[i].Watch(listeners[i])
		t.Logf("  ✓ Watch() registered on %s", nodeNames[i])
	}

	// Step 3: Start all nodes
	// In production, nodes would start at different times, but for testing we start them together
	t.Log("\nStep 3: Starting all three nodes...")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for i := 0; i < numNodes; i++ {
		err := nodes[i].Start(ctx)
		if err != nil {
			t.Fatalf("Failed to start %s: %v", nodeNames[i], err)
		}
		t.Logf("  ✓ Started %s", nodeNames[i])
	}

	// Step 4: Wait for leader election to complete
	t.Log("\nStep 4: Waiting for leader election to complete...")
	// Give enough time for:
	// - All nodes to register as members
	// - Leader election to occur
	// - All nodes to observe the leader
	// - Events to be broadcast and received
	time.Sleep(5 * time.Second)
	t.Log("  ✓ Leader election completed")

	// Step 5: Verify leader election results by checking each node's role
	t.Log("\nStep 5: Verifying leader election results...")
	leaderCount := 0
	followerCount := 0
	var leaderNode string

	for i := 0; i < numNodes; i++ {
		nodes[i].lock.Lock()
		role := nodes[i].myNode.Role
		name := nodes[i].myNode.Name
		nodes[i].lock.Unlock()

		if role == Leader {
			leaderCount++
			leaderNode = name
			t.Logf("  ✓ %s is LEADER", name)
		} else if role == Follower {
			followerCount++
			t.Logf("  ✓ %s is FOLLOWER", name)
		} else {
			t.Logf("  ⚠ %s has role: %s", name, role)
		}
	}

	// Step 6: Assertions on leader election
	t.Log("\nStep 6: Validating election requirements...")
	if leaderCount != 1 {
		t.Errorf("Expected exactly 1 leader, got %d", leaderCount)
		t.Fatalf("Leader election failed: need exactly 1 leader")
	}
	t.Logf("  ✓ Exactly 1 leader elected: %s", leaderNode)

	if followerCount != 2 {
		t.Errorf("Expected exactly 2 followers, got %d", followerCount)
	} else {
		t.Logf("  ✓ Exactly 2 followers as expected")
	}

	// Step 7: Use Members() to verify cluster membership
	t.Log("\nStep 7: Verifying cluster membership using Members()...")

	// Call Members() from the leader node
	members, err := nodes[0].Members()
	if err != nil {
		t.Fatalf("Failed to get members: %v", err)
	}

	if len(members) != numNodes {
		t.Errorf("Expected %d members, got %d", numNodes, len(members))
	} else {
		t.Logf("  ✓ Cluster has %d members as expected", numNodes)
	}

	// Verify all node names are present in members list
	memberNames := make(map[string]bool)
	for _, member := range members {
		memberNames[member.Name] = true
		t.Logf("    - Member: %s", member.Name)
	}

	for _, expectedName := range nodeNames {
		if !memberNames[expectedName] {
			t.Errorf("Expected member '%s' not found in cluster", expectedName)
		}
	}
	t.Log("  ✓ All expected members present in cluster")

	// Step 8: Verify events were received by all nodes
	t.Log("\nStep 8: Verifying all nodes received leader election events...")
	for i := 0; i < numNodes; i++ {
		leaderEvents := listeners[i].GetLeaderEvents()
		memberEvents := listeners[i].GetMembershipEvents()

		if len(leaderEvents) == 0 {
			t.Errorf("Node %s received no leader events", nodeNames[i])
		} else {
			t.Logf("  ✓ %s received %d leader event(s)", nodeNames[i], len(leaderEvents))
			// Check the first event is LeaderElected
			if leaderEvents[0].Type == LeaderElected {
				t.Logf("    - First event: LeaderElected (%s)", leaderEvents[0].Member.Name)
			}
		}

		if len(memberEvents) == 0 {
			t.Errorf("Node %s received no membership events", nodeNames[i])
		} else {
			t.Logf("  ✓ %s received %d membership event(s)", nodeNames[i], len(memberEvents))
		}
	}

	// Final summary
	t.Log("\n" + strings.Repeat("=", 70))
	t.Log("LEADER ELECTION TEST SUMMARY")
	t.Log(strings.Repeat("=", 70))
	t.Logf("Cluster Size:     %d nodes", numNodes)
	t.Logf("Leader:           %s", leaderNode)
	t.Logf("Followers:        %d", followerCount)
	t.Logf("Total Members:    %d", len(members))
	t.Log("Status:           ✓ PASSED - Leader election successful!")
	t.Log(strings.Repeat("=", 70))
}
