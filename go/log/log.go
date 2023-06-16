package log

import (
	"go.uber.org/zap"
)

var initialized bool

var Info func(msg string, args ...interface{})
var Warn func(msg string, args ...interface{})
var Error func(msg string, args ...interface{})
var Fatal func(msg string, args ...interface{})
var Debug func(msg string, args ...interface{})

func Init() {
	if initialized {
		return
	}

	logger, _ := zap.NewDevelopment()
	sugared := logger.Sugar()

	Info = sugared.Infow
	Warn = sugared.Warnw
	Error = sugared.Errorw
	Fatal = sugared.Fatalw
	Debug = sugared.Debugw

	initialized = true

	defer func(zapLogger *zap.Logger) {
		zapLogger.Sync()
	}(logger)
}
