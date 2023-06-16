package common

import (
	"beacon/go/log"
	"beacon/go/util"
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type Shutdown struct {
	Ctx       context.Context
	WaitGroup *sync.WaitGroup
}

func WithShutdown(fn func(sh Shutdown)) {
	shutdownWaitGroup := sync.WaitGroup{}
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	quitCh := make(chan os.Signal, 1)
	signal.Notify(quitCh, os.Interrupt, syscall.SIGTERM)

	sh := Shutdown{
		Ctx:       shutdownCtx,
		WaitGroup: &shutdownWaitGroup,
	}

	go func() {
		<-quitCh
		log.Info("Shutting down...")
		shutdown()
		time.Sleep(1 * time.Second)
	}()

	fn(sh)

	select {
	case <-util.WaitGroupToChan(&shutdownWaitGroup):
		log.Info("Wait group finished.")
	case <-shutdownCtx.Done():
		log.Info("Shutdown context called.")
	}

	log.Info("Exited.")
}
