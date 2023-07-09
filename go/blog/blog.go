package blog // beacon log... Yeah I know

import (
	"go.uber.org/zap"
)

var initialized bool

var noop = func(msg string, args ...interface{}) {}

var Info func(msg string, args ...interface{}) = noop
var Warn func(msg string, args ...interface{}) = noop
var Error func(msg string, args ...interface{}) = noop
var Fatal func(msg string, args ...interface{}) = noop
var Debug func(msg string, args ...interface{}) = noop

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
