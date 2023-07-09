package common

import (
	"beacon/go/blog"
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
	Shutdown  context.CancelFunc
}

func NewShutdown() Shutdown {
	shutdownWaitGroup := sync.WaitGroup{}
	shutdownCtx, shutdown := context.WithCancel(context.Background())

	return Shutdown{
		Ctx:       shutdownCtx,
		WaitGroup: &shutdownWaitGroup,
		Shutdown:  shutdown,
	}
}

func WithShutdown(fn func(sh Shutdown)) {
	sh := NewShutdown()
	quitCh := make(chan os.Signal, 1)
	signal.Notify(quitCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-quitCh
		blog.Info("Shutting down...")
		sh.Shutdown()
		time.Sleep(1 * time.Second)
	}()

	fn(sh)

	select {
	case <-util.WaitGroupToChan(sh.WaitGroup):
	case <-sh.Ctx.Done():
	}

	blog.Info("Exited.")
}
