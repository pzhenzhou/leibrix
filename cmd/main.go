package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pzhenzhou/leibri.io/internal/api/grpc"
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
	if err := run(); err != nil {
		logger.Error(err, "Application run failed")
		os.Exit(1)
	}
	logger.Info("Application shutdown complete")
}

// run orchestrates the entire application lifecycle, from startup to shutdown.
func run() error {
	configPath, ok := os.LookupEnv(common.ENVConfigFilePath)
	if !ok {
		configPath = defaultConfigPath
	}
	logger.Info("Loading configuration", "path", configPath)
	configObj, err := conf.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	logger.Info("Configuration loaded successfully",
		"node", configObj.Node.NodeName,
		"dataDir", configObj.Node.DataDir)

	nodeServer, election, grpcServer, grpcErrCh, err := startServices(context.Background(), configObj)
	if err != nil {
		return fmt.Errorf("failed to start services: %w", err)
	}
	logRuntimeInfo(configObj, nodeServer, election)
	reason, runErr := handleShutdown(grpcErrCh)
	if runErr != nil {
		logger.Error(runErr, "Shutdown initiated due to server error", "reason", reason)
	} else {
		logger.Info("Shutdown initiated", "reason", reason)
	}
	shutdownServices(configObj, nodeServer, election, grpcServer)
	return runErr
}

// startServices brings up the core application components: etcd, leader election, and gRPC server.
// It returns handles to the running services and a channel for the gRPC server error.
func startServices(ctx context.Context, configObj *conf.LeibrixConfig) (
	*cluster.LeibrixNodeServer,
	*cluster.LeibrixLeaderElection,
	*grpc.LeibrixGRPCServer,
	<-chan error,
	error,
) {
	// Start embedded etcd server
	logger.Info("Starting embedded etcd server", "node", configObj.Node.NodeName)
	nodeServer := cluster.NewLeibrixNodeServer(configObj)
	if err := nodeServer.Start(ctx, defaultServerStartTimeout); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("etcd server start failed: %w", err)
	}
	logger.Info("Embedded etcd server started successfully", "state", nodeServer.GetServerState())

	// Start leader election service
	logger.Info("Starting leader election service", "node", configObj.Node.NodeName)
	election, err := cluster.NewLeaderElection(configObj)
	if err != nil {
		_ = nodeServer.Stop(context.Background()) // Cleanup
		return nil, nil, nil, nil, fmt.Errorf("leader election init failed: %w", err)
	}
	if err := election.Start(ctx); err != nil {
		_ = nodeServer.Stop(context.Background()) // Cleanup
		_ = election.Close()
		return nil, nil, nil, nil, fmt.Errorf("leader election start failed: %w", err)
	}
	logger.Info("Leader election service started successfully")

	// Start gRPC server
	logger.Info("Starting gRPC server", "node", configObj.Node.NodeName)
	grpcServer, err := grpc.NewGRPCServer(configObj)
	if err != nil {
		_ = nodeServer.Stop(context.Background()) // Cleanup
		_ = election.Close()
		return nil, nil, nil, nil, fmt.Errorf("gRPC server init failed: %w", err)
	}

	grpcErrCh := make(chan error, 1)
	go func() {
		if err := grpcServer.Start(context.Background()); err != nil {
			grpcErrCh <- err
		}
	}()

	return nodeServer, election, grpcServer, grpcErrCh, nil
}

// logRuntimeInfo logs key operational details after services are successfully started.
func logRuntimeInfo(configObj *conf.LeibrixConfig, nodeServer *cluster.LeibrixNodeServer, election *cluster.LeibrixLeaderElection) {
	if members, err := election.Members(); err == nil {
		grpcAddress := fmt.Sprintf("%s:%d", configObj.Node.HostName, configObj.Node.RPCPort)
		logger.Info("Leibrix node is running",
			"node", configObj.Node.NodeName,
			"clusterSize", len(members),
			"grpcAddress", grpcAddress,
			"etcdState", nodeServer.GetServerState())
	}
}

// handleShutdown waits for a shutdown trigger (OS signal or gRPC error) and returns the reason.
func handleShutdown(grpcErrCh <-chan error) (reason string, err error) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	logger.Info("All services started, waiting for shutdown signal...")

	select {
	case sig := <-sigCh:
		return fmt.Sprintf("received signal: %s", sig), nil
	case err := <-grpcErrCh:
		return "gRPC server error", err
	}
}

// shutdownServices performs a graceful shutdown of all services in the reverse order of startup.
func shutdownServices(
	configObj *conf.LeibrixConfig,
	nodeServer *cluster.LeibrixNodeServer,
	election *cluster.LeibrixLeaderElection,
	grpcServer *grpc.LeibrixGRPCServer,
) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	logger.Info("Stopping gRPC server...")
	if err := grpcServer.Shutdown(shutdownCtx); err != nil {
		logger.Error(err, "gRPC server shutdown failed")
	} else {
		logger.Info("gRPC server stopped successfully")
	}

	logger.Info("Stopping leader election service...")
	if err := election.Close(); err != nil {
		logger.Error(err, "Leader election shutdown failed")
	} else {
		logger.Info("Leader election service stopped successfully")
	}

	logger.Info("Stopping embedded etcd server...")
	if err := nodeServer.Stop(shutdownCtx); err != nil {
		logger.Error(err, "Embedded etcd server shutdown failed")
	} else {
		logger.Info("Embedded etcd server stopped successfully")
	}
	logger.Info("Leibrix node shutdown sequence finished", "node", configObj.Node.NodeName)
}
