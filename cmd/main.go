package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pzhenzhou/leibri.io/internal/cluster"
	"github.com/pzhenzhou/leibri.io/internal/conf"
	"github.com/pzhenzhou/leibri.io/pkg/common"
)

const (
	defaultConfigPath         = "./config.yaml"
	defaultShutdownTimeout    = 30 * time.Second
	defaultServerStartTimeout = 60 * time.Second
)

var logger = common.InitLogger()

func main() {
	configPath, ok := os.LookupEnv(common.ENVConfigFilePath)
	if !ok {
		configPath = defaultConfigPath
	}
	logger.Info("Loading configuration", "path", configPath)
	configObj, loadConfigErr := conf.LoadConfig(configPath)
	if loadConfigErr != nil {
		logger.Error(loadConfigErr, "Failed to load configuration")
		os.Exit(1)
	}
	logger.Info("Configuration loaded successfully",
		"node", configObj.Node.NodeName,
		"dataDir", configObj.Node.DataDir)
	logger.Info("Starting embedded etcd server", "node", configObj.Node.NodeName)
	nodeServer := cluster.NewLeibrixNodeServer(configObj)
	ctx := context.Background()
	if err := nodeServer.Start(ctx, defaultServerStartTimeout); err != nil {
		logger.Error(err, "Failed to start embedded etcd server")
		os.Exit(1)
	}
	logger.Info("Embedded etcd server started successfully",
		"node", configObj.Node.NodeName,
		"state", nodeServer.GetServerState())
	logger.Info("Starting leader election service", "node", configObj.Node.NodeName)
	election, err := cluster.NewLeaderElection(configObj)
	if err != nil {
		logger.Error(err, "Failed to create leader election service")
		// Clean up etcd server before exit
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		nodeServer.Stop(shutdownCtx)
		cancel()
		os.Exit(1)
	}
	if err := election.Start(ctx); err != nil {
		logger.Error(err, "Failed to start leader election service")
		// Clean up both services before exit
		election.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		nodeServer.Stop(shutdownCtx)
		cancel()
		os.Exit(1)
	}

	logger.Info("Leader election service started successfully", "node", configObj.Node.NodeName)

	// Log cluster members for observability
	members, err := election.Members()
	if err != nil {
		logger.Error(err, "Failed to get cluster members")
	} else {
		logger.Info("Leibrix node is running",
			"node", configObj.Node.NodeName,
			"clusterSize", len(members))
	}

	// 4. Setup signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger.Info("Waiting for shutdown signal...")
	sig := <-sigCh
	logger.Info("Received shutdown signal, initiating graceful shutdown", "signal", sig)

	// 5. Graceful shutdown: stop application layer first, then infrastructure layer
	logger.Info("Stopping leader election service...")
	if err := election.Close(); err != nil {
		logger.Error(err, "Error during leader election shutdown")
	} else {
		logger.Info("Leader election service stopped successfully")
	}

	logger.Info("Stopping embedded etcd server...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	if err := nodeServer.Stop(shutdownCtx); err != nil {
		logger.Error(err, "Error during etcd server shutdown")
		os.Exit(1)
	}
	logger.Info("Embedded etcd server stopped successfully")
	logger.Info("Leibrix node shutdown complete", "node", configObj.Node.NodeName)
}
