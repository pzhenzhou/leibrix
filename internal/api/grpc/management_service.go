package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/pzhenzhou/leibri.io/internal/conf"
	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"
)

const (
	DefaultTimeout = 5 // seconds
)

type ManagementRPCService struct {
	myproto.UnimplementedManagementServiceServer
	config     *conf.LeibrixConfig
	etcdClient *clientv3.Client
}

func NewManagementService(config *conf.LeibrixConfig) (myproto.ManagementServiceServer, error) {
	etcdCfg := clientv3.Config{
		Endpoints:   config.ClusterConfig.ListenClientUrls,
		DialTimeout: DefaultTimeout * time.Second,
	}
	cli, err := clientv3.New(etcdCfg)
	if err != nil {
		logger.Error(err, "Failed to create etcd client")
		return nil, fmt.Errorf("failed to create etcd client: %w", err)
	}
	// Optional: Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout*time.Second)
	defer cancel()
	if _, err := cli.Status(ctx, config.ClusterConfig.ListenClientUrls[0]); err != nil {
		cli.Close()
		return nil, fmt.Errorf("failed to connect to etcd: %w", err)
	}
	logger.Info("etcd client initialized successfully", "endpoints", config.ClusterConfig.ListenClientUrls)

	return &ManagementRPCService{
		config:     config,
		etcdClient: cli,
	}, nil
}

func (m *ManagementRPCService) AdmitDataset(ctx context.Context, request *myproto.AdmitDatasetRequest) (*myproto.AdmitDatasetResponse, error) {
	return nil, nil
}

func (m *ManagementRPCService) UpsertTenantQuota(ctx context.Context, request *myproto.TenantQuota) (*myproto.CommonResponse, error) {
	if request.TenantId == "" {
		logger.Error(fmt.Errorf("tenant_id is empty"), "UpsertTenantQuota failed")
		return &myproto.CommonResponse{
			Status:  myproto.ResponseStatus_ERROR,
			Message: "tenant_id cannot be empty",
		}, nil
	}
	if request.MaxDatasets < 0 || request.MaxEpochs < 0 || request.MaxStorageMb < 0 {
		logger.Error(fmt.Errorf("invalid quota values"), "UpsertTenantQuota failed", "tenant_id", request.TenantId)
		return &myproto.CommonResponse{
			Status:  myproto.ResponseStatus_ERROR,
			Message: "quota values cannot be negative",
		}, nil
	}
	// convert maxStorageMb to maxStorageBytes
	// 2. Serialize with protobuf (native format)
	val, err := proto.Marshal(request)
	if err != nil {
		logger.Error(err, "Failed to serialize TenantQuota", "tenant_id", request.TenantId)
		return nil, err
	}
	tenantQuoteKey := fmt.Sprintf("/tenants/%s/quota", request.TenantId)
	putRsp, err := m.etcdClient.Put(ctx, tenantQuoteKey, string(val))
	if err != nil {
		logger.Error(err, "Failed to upsert TenantQuota in etcd", "tenant_id", request.TenantId)
		return nil, err
	}
	logger.Info("Successfully upserted TenantQuota", "tenant_id", request.TenantId, "revision", putRsp.Header.Revision)
	return &myproto.CommonResponse{
		Status: myproto.ResponseStatus_SUCCESS,
	}, nil
}

// Close gracefully closes the etcd client connection
func (m *ManagementRPCService) Close() error {
	if m.etcdClient != nil {
		logger.Info("Closing etcd client connection")
		return m.etcdClient.Close()
	}
	return nil
}
