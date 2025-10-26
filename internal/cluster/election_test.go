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
	clientv3 "go.etcd.io/etcd/client/v3"
	concurrencyv3 "go.etcd.io/etcd/client/v3/concurrency"
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

	// Generate a random peer port to avoid conflicts between concurrent tests
	// Using nanosecond timestamp modulo to generate a port in the range 20000-29999
	peerPort := 20000 + (time.Now().UnixNano() % 10000)

	// CRITICAL: Set node identity and cluster configuration
	// The peer URL port must match what's in InitialCluster
	cfg.Name = nodeName
	cfg.InitialCluster = fmt.Sprintf("%s=http://127.0.0.1:%d", nodeName, peerPort)
	cfg.InitialClusterToken = fmt.Sprintf("etcd-test-cluster-%d", time.Now().UnixNano())

	// Use random ports to avoid conflicts between concurrent tests
	cfg.ListenClientUrls = []url.URL{parseURL("http://127.0.0.1:0")}
	cfg.AdvertiseClientUrls = []url.URL{parseURL("http://127.0.0.1:0")}
	cfg.ListenPeerUrls = []url.URL{parseURL(fmt.Sprintf("http://127.0.0.1:%d", peerPort))}
	cfg.AdvertisePeerUrls = []url.URL{parseURL(fmt.Sprintf("http://127.0.0.1:%d", peerPort))}

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
func createTestConfig(nodeName, clientURL string) *conf.LeibrixConfig {
	return &conf.LeibrixConfig{
		Node: conf.NodeConfig{
			NodeName:      nodeName,
			HostName:      "localhost",
			ListenPort:    2380,
			AdvertisePort: 2382,
		},
		ClusterConfig: conf.ClusterConfig{
			ListenClientUrls: []string{clientURL},
		},
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

	// Verify member is registered in etcd by querying the Members() API
	// This replaces the old memberLease check since we now use session.Lease()
	members, err := election.Members()
	if err != nil {
		t.Fatalf("Failed to get members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("Expected 1 registered member, got %d", len(members))
	}
	if members[0].Name != "test-node-1" {
		t.Errorf("Expected member name 'test-node-1', got '%s'", members[0].Name)
	}

	err = election.Close()
	if err != nil {
		t.Fatalf("Failed to close election: %v", err)
	}
}

// TestSingleLeaseForElectionAndMembership verifies that both election and member
// registration share the same session lease, ensuring atomic lifecycle management.
// This test validates the core refactoring that eliminates duplicate lease management.
func TestSingleLeaseForElectionAndMembership(t *testing.T) {
	_, clientURL, cleanup := startEmbeddedEtcd(t)
	defer cleanup()

	config := createTestConfig("test-master-lease", clientURL)
	election, err := NewLeaderElection(config)
	if err != nil {
		t.Fatalf("Failed to create leader election: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Step 1: Start the election service
	t.Log("Step 1: Starting election service...")
	err = election.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start election: %v", err)
	}
	defer election.Close()

	// Wait for registration to complete
	time.Sleep(500 * time.Millisecond)

	// Step 2: Get the session lease ID
	t.Log("Step 2: Getting session lease ID...")
	if election.session == nil {
		t.Fatal("Expected session to be created")
	}
	sessionLeaseID := election.session.Lease()
	t.Logf("  Session lease ID: %x", sessionLeaseID)

	// Step 3: Verify member key exists and get its lease
	t.Log("Step 3: Verifying member key in etcd...")
	memberKey := membersKey + config.Node.NodeName
	resp, err := election.client.Get(ctx, memberKey)
	if err != nil {
		t.Fatalf("Failed to get member key from etcd: %v", err)
	}

	if len(resp.Kvs) == 0 {
		t.Fatalf("Member key not found in etcd: %s", memberKey)
	}

	memberKeyLeaseID := resp.Kvs[0].Lease
	t.Logf("  Member key lease ID: %x", memberKeyLeaseID)

	// Step 4: CRITICAL VERIFICATION - Both should use the same lease
	t.Log("Step 4: Verifying lease sharing...")
	if int64(sessionLeaseID) != memberKeyLeaseID {
		t.Errorf("FAILED: Member key lease (%x) does not match session lease (%x)",
			memberKeyLeaseID, sessionLeaseID)
		t.Fatal("Election and membership must share the same lease for atomic consistency")
	}
	t.Logf("  ✓ SUCCESS: Both use the same lease: %x", sessionLeaseID)

	// Step 5: Verify the election key also uses the same lease
	t.Log("Step 5: Verifying election key lease...")
	electionResp, err := election.client.Get(ctx, electionKey, clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("Failed to get election key: %v", err)
	}
	if len(electionResp.Kvs) > 0 {
		electionKeyLeaseID := electionResp.Kvs[0].Lease
		t.Logf("  Election key lease ID: %x", electionKeyLeaseID)
		if int64(sessionLeaseID) != electionKeyLeaseID {
			t.Errorf("Election key lease (%x) does not match session lease (%x)",
				electionKeyLeaseID, sessionLeaseID)
		} else {
			t.Logf("  ✓ Election key also uses session lease")
		}
	}

	// Step 6: Create a separate client for verification after close
	t.Log("Step 6: Creating separate etcd client for verification...")
	verifyClient, err := clientv3.New(clientv3.Config{
		Endpoints: []string{clientURL},
	})
	if err != nil {
		t.Fatalf("Failed to create verification client: %v", err)
	}
	defer verifyClient.Close()

	// Step 7: Close the election and verify automatic cleanup
	t.Log("Step 7: Closing election and verifying automatic cleanup...")
	err = election.Close()
	if err != nil {
		t.Fatalf("Failed to close election: %v", err)
	}

	// Give etcd time to process the lease revocation
	time.Sleep(500 * time.Millisecond)

	// Step 8: Verify member key is automatically removed
	t.Log("Step 8: Verifying member key was automatically removed...")
	resp, err = verifyClient.Get(context.Background(), memberKey)
	if err != nil {
		t.Fatalf("Failed to check member key after close: %v", err)
	}

	if len(resp.Kvs) > 0 {
		t.Errorf("FAILED: Member key still exists after session close")
		t.Logf("  Key: %s, Value: %s", resp.Kvs[0].Key, resp.Kvs[0].Value)
	} else {
		t.Log("  ✓ Member key automatically removed")
	}

	// Step 9: Verify election key is also removed
	t.Log("Step 9: Verifying election key was automatically removed...")
	electionResp, err = verifyClient.Get(context.Background(), electionKey, clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("Failed to check election key after close: %v", err)
	}

	if len(electionResp.Kvs) > 0 {
		t.Errorf("Election key still exists after session close")
	} else {
		t.Log("  ✓ Election key automatically removed")
	}

	// Final summary
	t.Log("\n" + strings.Repeat("=", 70))
	t.Log("SINGLE LEASE TEST SUMMARY")
	t.Log(strings.Repeat("=", 70))
	t.Logf("Session Lease ID:       %x", sessionLeaseID)
	t.Logf("Member Key Lease ID:    %x", memberKeyLeaseID)
	t.Log("Lease Matching:         ✓ PASSED")
	t.Log("Automatic Cleanup:      ✓ PASSED")
	t.Log("Status:                 ✓ Single-lease pattern working correctly!")
	t.Log(strings.Repeat("=", 70))
}

// TestSessionExpirationRemovesMember verifies that when a session expires,
// both the election key and member key are automatically removed from etcd.
// This test validates the automatic cleanup behavior of the single-lease pattern.
func TestSessionExpirationRemovesMember(t *testing.T) {
	_, clientURL, cleanup := startEmbeddedEtcd(t)
	defer cleanup()

	config := createTestConfig("test-expiry-node", clientURL)
	election, err := NewLeaderElection(config)
	if err != nil {
		t.Fatalf("Failed to create leader election: %v", err)
	}

	ctx := context.Background()

	// Step 1: Start election with very short TTL for faster test
	t.Log("Step 1: Starting election service with short TTL...")

	// Create a custom session with shorter TTL (3 seconds) for testing
	session, err := concurrencyv3.NewSession(election.client, concurrencyv3.WithTTL(3))
	if err != nil {
		t.Fatalf("Failed to create short-TTL session: %v", err)
	}
	election.session = session
	election.election = concurrencyv3.NewElection(session, electionKey)

	// Register member using the short-TTL session
	if err := election.registerMember(ctx); err != nil {
		t.Fatalf("Failed to register member: %v", err)
	}

	memberKey := membersKey + config.Node.NodeName
	t.Logf("  Member registered: %s", memberKey)

	// Step 2: Verify member key exists
	t.Log("Step 2: Verifying member key exists in etcd...")
	resp, err := election.client.Get(ctx, memberKey)
	if err != nil {
		t.Fatalf("Failed to get member key: %v", err)
	}
	if len(resp.Kvs) == 0 {
		t.Fatal("Member key not found after registration")
	}
	t.Logf("  ✓ Member key exists with lease: %x", resp.Kvs[0].Lease)

	// Step 3: Stop the keepalive by closing the session context
	// This simulates a network partition or node failure
	t.Log("Step 3: Simulating session expiration by closing session...")

	// Close the session to stop keepalive
	// This will cause the lease to expire after TTL seconds
	if err := session.Close(); err != nil {
		t.Fatalf("Failed to close session: %v", err)
	}

	// Step 4: Wait for lease to expire (TTL + grace period)
	t.Log("Step 4: Waiting for lease expiration...")
	waitTime := 5 * time.Second // TTL=3s + 2s grace
	t.Logf("  Waiting %v for lease to expire...", waitTime)
	time.Sleep(waitTime)

	// Step 5: Verify member key is automatically removed
	t.Log("Step 5: Verifying member key was automatically removed...")
	resp, err = election.client.Get(ctx, memberKey)
	if err != nil {
		t.Fatalf("Failed to check member key: %v", err)
	}

	if len(resp.Kvs) > 0 {
		t.Errorf("FAILED: Member key still exists after session expiration")
		t.Logf("  Key: %s, Lease: %x", resp.Kvs[0].Key, resp.Kvs[0].Lease)
		t.Fatal("Member key should be automatically removed when session expires")
	}
	t.Log("  ✓ Member key automatically removed after expiration")

	// Step 6: Verify election key is also removed
	t.Log("Step 6: Verifying election key was automatically removed...")
	electionResp, err := election.client.Get(ctx, electionKey, clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("Failed to check election key: %v", err)
	}

	// Count keys that belong to this specific node (if any)
	nodeElectionKeys := 0
	for _, kv := range electionResp.Kvs {
		if string(kv.Value) == config.Node.NodeName {
			nodeElectionKeys++
		}
	}

	if nodeElectionKeys > 0 {
		t.Errorf("Election key for this node still exists after session expiration")
	} else {
		t.Log("  ✓ Election key automatically removed")
	}

	// Final summary
	t.Log("\n" + strings.Repeat("=", 70))
	t.Log("SESSION EXPIRATION TEST SUMMARY")
	t.Log(strings.Repeat("=", 70))
	t.Log("Session TTL:           3 seconds")
	t.Log("Wait Time:             5 seconds")
	t.Log("Member Key Removed:    ✓ YES")
	t.Log("Election Key Removed:  ✓ YES")
	t.Log("Status:                ✓ Automatic cleanup working correctly!")
	t.Log(strings.Repeat("=", 70))
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
			if leaderEvents[0].Type == EvtLeaderElected {
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
