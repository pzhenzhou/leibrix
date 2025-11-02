package common

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

const (
	ENVKeyLeibrixRuntime = "LEIBRIX_RUNTIME"
	ENVConfigFilePath    = "LEIBRIX_CONFIG_PATH"
)

func IsProdRuntime() bool {
	runEvnVal, hasEnv := os.LookupEnv(ENVKeyLeibrixRuntime)
	if hasEnv {
		return strings.Compare(strings.ToLower(runEvnVal), "prod") == 0
	} else {
		return false
	}
}

func InitLogger() logr.Logger {
	zapLogger, err := BuildZapLogger()
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize zap logger %v", err))
	}
	return zapr.NewLogger(zapLogger)
}

func BuildZapLogger() (*zap.Logger, error) {
	var logConfig zap.Config
	if IsProdRuntime() {
		logConfig = zap.NewProductionConfig()
		logConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	} else {
		logConfig = zap.NewDevelopmentConfig()
		logConfig.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	}
	return logConfig.Build()
}

func EtcdTenantQuotaKey(tenantId string) string {
	return fmt.Sprintf("/leibrix/tenants/%s/quota", tenantId)
}

func EtcdTenantDatasetEpochKey(tenantId, dataSetId string) string {
	return fmt.Sprintf("/leibrix/tenants/%s/datasets/%s", tenantId, dataSetId)
}

func DefaultEtcdClientConfig(endpoint []string) clientv3.Config {
	return clientv3.Config{
		Endpoints:            endpoint,
		DialTimeout:          5 * time.Second,
		DialKeepAliveTime:    2 * time.Second,
		DialKeepAliveTimeout: 2 * time.Second,
		AutoSyncInterval:     30 * time.Second,
		MaxCallSendMsgSize:   16 * 1024 * 1024,
		MaxCallRecvMsgSize:   16 * 1024 * 1024,
	}
}

func NewEtcdClient(endpoint []string) (*clientv3.Client, error) {
	return clientv3.New(DefaultEtcdClientConfig(endpoint))
}
