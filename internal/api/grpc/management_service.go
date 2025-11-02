package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/pzhenzhou/leibri.io/internal/conf"
	"github.com/pzhenzhou/leibri.io/pkg/common"
	myproto "github.com/pzhenzhou/leibri.io/pkg/proto"
	"github.com/samber/lo"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"
)

type ManagementRPCService struct {
	myproto.UnimplementedManagementServiceServer
	config     *conf.LeibrixConfig
	etcdClient *clientv3.Client
}

func NewManagementService(config *conf.LeibrixConfig) (myproto.ManagementServiceServer, error) {
	cli, err := common.NewEtcdClient(config.ClusterConfig.ListenClientUrls)
	if err != nil {
		logger.Error(err, "Failed to create etcd client")
		return nil, fmt.Errorf("failed to create etcd client: %w", err)
	}
	// Optional: Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Status(ctx, config.ClusterConfig.ListenClientUrls[0]); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("failed to connect to etcd: %w", err)
	}
	logger.Info("etcd client initialized successfully", "endpoints", config.ClusterConfig.ListenClientUrls)

	return &ManagementRPCService{
		config:     config,
		etcdClient: cli,
	}, nil
}

func (m *ManagementRPCService) AdmitDataset(ctx context.Context, request *myproto.AdmitDatasetRequest) (*myproto.AdmitDatasetResponse, error) {
	logger.Info("ManagementRPCService AdmitDataset called", "clientIp", getClientIp(ctx))
	if err := m.validateAdmitRequest(request); err != nil {
		logger.Error(err, "Invalid AdmitDataset request")
		return &myproto.AdmitDatasetResponse{
			Status:  myproto.AdmitDatasetResponse_REJECTED,
			Message: fmt.Sprintf("validation failed: %v", err),
		}, nil
	}
	// Check tenant quota
	tenantId := request.GetTenantId()
	tenantQuota, _, getErr := GetProtoMessage(ctx, m.etcdClient, common.EtcdTenantQuotaKey(tenantId), &myproto.TenantQuota{})
	if getErr != nil {
		logger.Error(getErr, "Failed to get TenantQuota from etcd", "tenant_id", tenantId)
		return &myproto.AdmitDatasetResponse{
			Status:  myproto.AdmitDatasetResponse_REJECTED,
			Message: fmt.Sprintf("failed to retrieve tenant quota: %v", getErr),
		}, nil
	}
	logger.Info("Retrieved tenant quota", "tenant_id", tenantId, "max_datasets", tenantQuota.MaxDatasets)
	// Generate epochs from the request
	generator := &EpochGenerator{}
	epochs, err := generator.GenerateEpochs(request)
	if err != nil {
		logger.Error(err, "Failed to generate epochs", "tenant_id", tenantId, "dataset_id", request.DatasetId)
		return &myproto.AdmitDatasetResponse{
			Status:  myproto.AdmitDatasetResponse_REJECTED,
			Message: fmt.Sprintf("epoch generation failed: %v", err),
		}, nil
	}
	logger.Info("Generated epochs",
		"tenant_id", tenantId,
		"dataset_id", request.DatasetId,
		"epoch_count", len(epochs))
	if tenantQuota.MaxEpochs > 0 && int32(len(epochs)) > tenantQuota.MaxEpochs {
		logger.Error(nil, "Epoch count exceeds tenant quota",
			"tenant_id", tenantId,
			"requested_epochs", len(epochs),
			"max_epochs", tenantQuota.MaxEpochs)
		return &myproto.AdmitDatasetResponse{
			Status: myproto.AdmitDatasetResponse_REJECTED,
			Message: fmt.Sprintf("epoch count %d exceeds tenant quota %d",
				len(epochs), tenantQuota.MaxEpochs),
		}, nil
	}
	saveEpochErr := m.saveEpochs(ctx, request, epochs)
	if saveEpochErr != nil {
		return nil, saveEpochErr
	}
	// TODO: Step Trigger worker assignment for data loading (etcd watch)
	logger.Info("Dataset admission accepted",
		"tenant_id", tenantId,
		"dataset_id", request.DatasetId,
		"epochs", len(epochs))
	return &myproto.AdmitDatasetResponse{
		Status: myproto.AdmitDatasetResponse_ACCEPTED,
		Epochs: &myproto.EpochInfoList{
			Epochs: epochs,
		},
	}, nil
}

func (m *ManagementRPCService) saveEpochs(ctx context.Context, request *myproto.AdmitDatasetRequest, epochs []*myproto.EpochInfo) error {
	epochListKey := common.EtcdTenantDatasetEpochKey(request.TenantId, request.DatasetId)
	// Get existing epochs
	existEpochList, getRsp, err := GetProtoMessage(context.Background(), m.etcdClient, epochListKey, &myproto.EpochInfoList{})
	if err != nil {
		if getRsp != nil && len(getRsp.Kvs) == 0 {
			_, putErr := PutProtoMessage(ctx, m.etcdClient, epochListKey, &myproto.EpochInfoList{
				Epochs: epochs,
			})
			if putErr != nil {
				logger.Error(putErr, "Failed to save new Epochs to etcd", "tenant_id", request.TenantId, "dataset_id", request.DatasetId)
				return putErr
			}
			return nil
		}
		return fmt.Errorf("failed to get existing epoch list: %w", err)
	}
	left, right := lo.Difference(existEpochList.Epochs, epochs)
	if len(left) == 0 && len(right) == 0 {
		logger.Info("No new epochs to save", "tenant_id", request.TenantId, "dataset_id", request.DatasetId)
		return nil
	}
	epochList := &myproto.EpochInfoList{
		Epochs: epochs,
	}
	if len(left) > 0 {
		epochList.Epochs = append(epochList.Epochs, left...)
	}
	epochListBytes, _ := proto.Marshal(epochList)
	txn := m.etcdClient.Txn(context.Background())
	commit, commitErr := txn.If(clientv3.Compare(clientv3.ModRevision(epochListKey), "=", getRsp.Kvs[0].ModRevision)).
		Then(clientv3.OpPut(epochListKey, string(epochListBytes))).Else(clientv3.OpGet(epochListKey)).Commit()
	if commitErr != nil {
		return commitErr
	}
	if !commit.Succeeded {
		logger.Error(fmt.Errorf("concurrent modification detected"), "Failed to save Epochs to etcd due to concurrent modification", "tenant_id", request.TenantId, "dataset_id", request.DatasetId)
		return fmt.Errorf("failed to save epoch list due to concurrent modification")
	}
	logger.Info("Successfully saved epochs to etcd", "tenant_id", request.TenantId, "dataset_id", request.DatasetId, "epoch_count", len(epochs))
	return nil
}

// validateAdmitRequest performs basic validation on the admission request
func (m *ManagementRPCService) validateAdmitRequest(request *myproto.AdmitDatasetRequest) error {
	if request.TenantId == "" {
		return fmt.Errorf("tenant_id is required")
	}
	if request.DatasetId == "" {
		return fmt.Errorf("dataset_id is required")
	}
	if request.Source == nil {
		return fmt.Errorf("source is required")
	}
	return nil
}

func (m *ManagementRPCService) UpsertTenantQuota(ctx context.Context, request *myproto.TenantQuota) (*myproto.CommonResponse, error) {
	logger.Info("ManagementRPCService UpsertTenantQuota called", "clientIp", getClientIp(ctx))
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
	tenantQuoteKey := common.EtcdTenantQuotaKey(request.TenantId)
	putRsp, putErr := PutProtoMessage(ctx, m.etcdClient, tenantQuoteKey, request)
	if putErr != nil {
		logger.Error(putErr, "Failed to upsert TenantQuota in etcd", "tenant_id", request.TenantId)
		return nil, putErr
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
