package cluster

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/pzhenzhou/leibri.io/internal/conf"
	"go.etcd.io/etcd/server/v3/embed"
)

type TypeServerState int32

const (
	ServerStateInit TypeServerState = iota << 4
	ServerStateReady
	ServerStateStopped
)

const (
	DefaultQuotaBackendBytes                 = 6 << 30
	DefaultCompactionRetention               = "100000"
	ServerStartDeadline        time.Duration = 60 * time.Second
)

var sererStateMapping = map[TypeServerState]string{
	ServerStateInit:    "init",
	ServerStateReady:   "ready",
	ServerStateStopped: "stopped",
}

// detectClusterState determines whether this node should bootstrap a new cluster
// or rejoin an existing cluster by examining the etcd data directory state.
//
// Returns "new" if the member directory is completely absent (fresh start).
// Returns "existing" if valid etcd data structures exist (WAL files or snapshot DB).
// Panics if the data directory is in a corrupted or inconsistent state.
//
// This enables idempotent configuration: the same config file works for both
// initial cluster creation and subsequent restarts without manual changes.
func detectClusterState(dataDir string) string {
	memberDir := filepath.Join(dataDir, "member")
	walDir := filepath.Join(memberDir, "wal")
	snapDir := filepath.Join(memberDir, "snap")
	snapDB := filepath.Join(snapDir, "db")

	// Check if member directory exists
	memberInfo, err := os.Stat(memberDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No member directory → completely fresh start
			return embed.ClusterStateFlagNew
		}
		// Permission denied or other filesystem error
		panic(fmt.Sprintf("failed to access member directory %s: %v", memberDir, err))
	}

	if !memberInfo.IsDir() {
		// member/ exists but is not a directory → corruption
		panic(fmt.Sprintf("member path exists but is not a directory: %s", memberDir))
	}

	// Member directory exists → check for initialized state
	hasWALFiles := hasFilesInDirectory(walDir)
	hasSnapshotDB := hasNonEmptyFile(snapDB)

	if hasWALFiles || hasSnapshotDB {
		// Valid etcd data found → rejoin existing cluster
		return embed.ClusterStateFlagExisting
	}

	// Member directory exists but contains no valid data
	// This indicates interrupted initialization or corruption
	// SAFETY: Do NOT silently treat as "new" - this can cause split-brain
	panic(fmt.Sprintf(
		"member directory exists but contains no valid etcd data (no WAL files, no snapshot DB). "+
			"This indicates interrupted initialization or corruption. "+
			"Please manually remove %s to re-bootstrap, or restore from backup.",
		memberDir,
	))
}

// hasFilesInDirectory checks if a directory exists and contains at least one file.
// Returns false if directory doesn't exist or is empty.
func hasFilesInDirectory(dir string) bool {
	hasFile := false

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// If root directory doesn't exist or is inaccessible, return the error
			if path == dir {
				return err
			}
			// Skip inaccessible subdirectories but continue walking
			return filepath.SkipDir
		}

		// Found a regular file → stop walking
		if !d.IsDir() {
			hasFile = true
			return filepath.SkipAll
		}

		return nil
	})

	// If directory doesn't exist, err will be non-nil
	if err != nil {
		return false
	}

	return hasFile
}

// hasNonEmptyFile checks if a file exists and has non-zero size.
// Returns false if file doesn't exist, is empty, or is not a regular file.
func hasNonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	if !info.Mode().IsRegular() {
		return false
	}

	return info.Size() > 0
}

type LeibrixNodeServer struct {
	config      *conf.LeibrixConfig
	embedEtcd   *embed.Etcd
	serverState int32
}

func NewLeibrixNodeServer(config *conf.LeibrixConfig) *LeibrixNodeServer {
	return &LeibrixNodeServer{
		config:      config,
		serverState: int32(ServerStateInit),
	}
}

// buildEtcdConfig translates the application's LeibrixConfig into an embed.Config
// suitable for launching an embedded etcd server. It configures node identity,
// cluster topology, communication URLs, and performance settings.
func buildEtcdConfig(config *conf.LeibrixConfig) (*embed.Config, error) {
	cfg := embed.NewConfig()
	cfg.Name = config.Node.NodeName
	cfg.Dir = config.Node.DataDir

	clusterConfig := config.ClusterConfig
	cfg.InitialClusterToken = clusterConfig.InitialClusterToken
	cfg.InitialCluster = clusterConfig.InitialCluster
	cfg.ClusterState = detectClusterState(config.Node.DataDir)

	// 3. Client URLs (for application clients)
	clientURLs, err := parseURLs(config.ClusterConfig.ListenClientUrls)
	if err != nil {
		return nil, fmt.Errorf("invalid client URLs: %w", err)
	}
	cfg.ListenClientUrls = clientURLs
	cfg.AdvertiseClientUrls = clientURLs // Keep it simple: listen and advertise on the same URLs

	// 4. Peer URLs (for etcd-to-etcd Raft communication)
	peerURLs, err := parseURLs(config.ClusterConfig.AdvertisePeerUrls)
	if err != nil {
		return nil, fmt.Errorf("invalid peer URLs: %w", err)
	}
	cfg.ListenPeerUrls = peerURLs
	cfg.AdvertisePeerUrls = peerURLs // Keep it simple: listen and advertise on the same URLs
	// Set heartbeat, defaulting to etcd's 100ms if not provided.
	if config.ClusterConfig.HeartbeatMs > 0 {
		cfg.TickMs = config.ClusterConfig.HeartbeatMs
	} else {
		cfg.TickMs = 100
	}

	// Set election timeout, defaulting to etcd's 1000ms if not provided.
	electionMs := config.ClusterConfig.ElectionMs
	if electionMs == 0 {
		electionMs = 3000
	}
	// Validate that the election timeout is sufficiently larger than the heartbeat.
	// This prevents misconfigurations that could lead to cluster instability.
	// Etcd's internal minimum is 5, but 10 is the recommended ratio.
	minRequiredElectionMs := 10 * cfg.TickMs
	if electionMs < minRequiredElectionMs {
		return nil, fmt.Errorf(
			"invalid configuration: ElectionMs (%dms) must be at least 10 times HeartbeatMs (%dms). Minimum required: %dms",
			electionMs,
			cfg.TickMs,
			minRequiredElectionMs,
		)
	}
	cfg.AutoCompactionMode = "revision"
	cfg.AutoCompactionRetention = DefaultCompactionRetention
	cfg.QuotaBackendBytes = DefaultQuotaBackendBytes
	cfg.ElectionMs = electionMs
	cfg.LogLevel = config.ClusterConfig.LogLevel
	return cfg, nil
}

// parseURLs is a helper to convert a slice of string URLs to a slice of url.URL.
func parseURLs(urlList []string) ([]url.URL, error) {
	urls := make([]url.URL, 0, len(urlList))
	for _, urlStr := range urlList {
		parsedURL, err := url.Parse(urlStr)
		if err != nil {
			return nil, fmt.Errorf("invalid URL %q: %w", urlStr, err)
		}
		urls = append(urls, *parsedURL)
	}
	return urls, nil
}

func (s *LeibrixNodeServer) GetServerState() string {
	return sererStateMapping[TypeServerState(s.serverState)]
}

func (s *LeibrixNodeServer) Start(rootCtx context.Context, startTimeout time.Duration) error {
	if atomic.LoadInt32(&s.serverState) != int32(ServerStateInit) {
		logger.Info("LeibrixNodeServer is already running", "node", s.config.Node.NodeName, "state", s.serverState)
		return nil
	}
	cfg, err := buildEtcdConfig(s.config)
	if err != nil {
		logger.Error(err, "failed to build etcd config", "config", s.config)
		return err
	}
	e, starErr := embed.StartEtcd(cfg)
	if starErr != nil {
		logger.Error(starErr, "failed to start etcd server", "config", s.config)
		return starErr
	}

	if startTimeout <= 0 {
		startTimeout = ServerStartDeadline
	}
	ctx, cancel := context.WithTimeout(rootCtx, startTimeout)
	defer cancel()
	select {
	case <-e.Server.ReadyNotify():
		s.embedEtcd = e
		atomic.AddInt32(&s.serverState, int32(ServerStateReady))
		return nil
	case <-ctx.Done():
		// cancel/timeout during startup: stop and return
		e.Close()
		return ctx.Err()
	}
}

// Stop gracefully shuts down the embedded etcd server.
// It waits for the server to fully stop with the provided context timeout.
func (s *LeibrixNodeServer) Stop(ctx context.Context) error {
	currentState := atomic.LoadInt32(&s.serverState)

	// Check if server is not running
	if currentState != int32(ServerStateReady) {
		logger.Info("LeibrixNodeServer is not running, skip stop",
			"node", s.config.Node.NodeName,
			"state", sererStateMapping[TypeServerState(currentState)])
		return nil
	}

	// Check if embedEtcd exists
	if s.embedEtcd == nil {
		logger.Error(nil, "embedEtcd is nil but state is ready, inconsistent state detected")
		atomic.StoreInt32(&s.serverState, int32(ServerStateStopped))
		return fmt.Errorf("inconsistent server state: embedEtcd is nil")
	}

	logger.Info("Stopping LeibrixNodeServer", "node", s.config.Node.NodeName)
	// Close the etcd server
	s.embedEtcd.Close()
	// Wait for server to stop with context timeout
	select {
	case <-s.embedEtcd.Server.StopNotify():
		atomic.StoreInt32(&s.serverState, int32(ServerStateStopped))
		logger.Info("LeibrixNodeServer stopped successfully", "node", s.config.Node.NodeName)
		return nil
	case <-ctx.Done():
		// Timeout or cancellation - still mark as stopped
		atomic.StoreInt32(&s.serverState, int32(ServerStateStopped))
		logger.Error(ctx.Err(), "LeibrixNodeServer stop timed out", "node", s.config.Node.NodeName)
		return nil
	}
}
