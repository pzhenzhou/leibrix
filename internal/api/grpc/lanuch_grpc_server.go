package grpc

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/pzhenzhou/leibri.io/internal/conf"
	"github.com/pzhenzhou/leibri.io/pkg/common"
	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

var (
	logger   = common.InitLogger()
	grpcOpts = []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     15 * time.Minute,
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 5 * time.Second,
			Time:                  5 * time.Second,
			Timeout:               1 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             5 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.MaxRecvMsgSize(128 * 1024 * 1024),
		grpc.MaxSendMsgSize(128 * 1024 * 1024),
	}
)

// LeibrixGRPCServer wraps the gRPC server and provides lifecycle management
type LeibrixGRPCServer struct {
	config        *conf.LeibrixConfig
	grpcServer    *grpc.Server
	healthServer  *health.Server
	managementSvc myproto.ManagementServiceServer
	listenAddr    string
}

// NewGRPCServer creates a new gRPC server instance
func NewGRPCServer(config *conf.LeibrixConfig) (*LeibrixGRPCServer, error) {
	grpcServer := grpc.NewServer(grpcOpts...)
	// Initialize ManagementService
	managementSvc, err := NewManagementService(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create ManagementService: %w", err)
	}
	// Register services
	myproto.RegisterManagementServiceServer(grpcServer, managementSvc)
	logger.Info("ManagementService registered successfully")
	// Register health check service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("leibrix.ManagementService", grpc_health_v1.HealthCheckResponse_SERVING)
	logger.Info("Health check service registered")
	// Register reflection service for debugging
	reflection.Register(grpcServer)
	logger.Info("Reflection service registered")
	listenAddr := fmt.Sprintf("%s:%d", config.Node.HostName, config.Node.RPCPort)
	return &LeibrixGRPCServer{
		config:        config,
		grpcServer:    grpcServer,
		healthServer:  healthServer,
		managementSvc: managementSvc,
		listenAddr:    listenAddr,
	}, nil
}

// Start starts the gRPC server and blocks until context is cancelled
func (s *LeibrixGRPCServer) Start(ctx context.Context) error {
	logger.Info("Starting Leibrix Master gRPC server",
		"address", s.listenAddr,
		"node", s.config.Node.NodeName)
	lis, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		logger.Error(err, "Failed to listen", "address", s.listenAddr)
		return fmt.Errorf("failed to listen on %s: %w", s.listenAddr, err)
	}
	serverErrCh := make(chan error, 1)
	go func() {
		logger.Info("gRPC server listening", "address", s.listenAddr)
		if err := s.grpcServer.Serve(lis); err != nil {
			logger.Error(err, "gRPC server failed")
			serverErrCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		logger.Info("gRPC server context cancelled, initiating shutdown")
		// The actual shutdown completion is handled by Shutdown() method
		s.initiateShutdown()
		return ctx.Err()
	case err := <-serverErrCh:
		return fmt.Errorf("server error: %w", err)
	}
}

// Shutdown gracefully shuts down the gRPC server with a timeout
func (s *LeibrixGRPCServer) Shutdown(ctx context.Context) error {
	logger.Info("Shutting down gRPC server...")
	// Mark service as not serving
	s.healthServer.SetServingStatus("leibrix.ManagementService", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	// Close the management service (closes etcd client)
	if closer, ok := s.managementSvc.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			logger.Error(err, "Error closing ManagementService")
		}
	}
	// Graceful stop with context timeout
	stopped := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
		logger.Info("gRPC server stopped gracefully")
		return nil
	case <-ctx.Done():
		logger.Info("Graceful shutdown timeout, forcing stop", "timeout", ctx.Err())
		s.grpcServer.Stop()
		return ctx.Err()
	}
}

// initiateShutdown marks the service as not serving and closes connections
// This is a non-blocking operation that prepares for shutdown
func (s *LeibrixGRPCServer) initiateShutdown() {
	s.healthServer.SetServingStatus("leibrix.ManagementService", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
}

// Stop immediately stops the gRPC server
func (s *LeibrixGRPCServer) Stop() {
	s.grpcServer.Stop()
}

// LeibrixMasterGRPCServer launches the gRPC server for the Leibrix Master node.
func LeibrixMasterGRPCServer(config *conf.LeibrixConfig) error {
	server, err := NewGRPCServer(config)
	if err != nil {
		return err
	}
	// Run with background context (will handle its own signals)
	return server.Start(context.Background())
}
