package common

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
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
