package retry

import (
	"beacon/go/blog"
	"beacon/go/common"
	"beacon/go/db"
	"beacon/go/util"
	"time"
)

type RetryConnector interface {
	AddRetry(p db.EventPayload, response string) error
	GetRetriesTriedLessThan(times int) ([]Retry, error)
	DeleteRetry(id int) error
	BumpRetry(id int, response string) error
}

type Retry struct {
	Id           int
	TryCount     int
	EventPayload db.EventPayload
	LastResponse string
	LastTryAt    time.Time
	DeferredAt   time.Time
}

type RetryEvent struct {
	Retry Retry
	Event db.EventPayload
}

type Retrier interface {
	Initialize()
	ScheduleRetry(event db.EventPayload, lastResult string) error
	BumpRetry(id int, lastResult string) error
	DeleteRetry(id int) error
	Subscribe(cb func(e RetryEvent)) int
}

type RetrierImpl struct {
	conn            RetryConnector
	sh              common.Shutdown
	ps              *util.PubSub[RetryEvent]
	intervalSeconds int
	maxRetries      int
}

func NewRetrier(conn RetryConnector, sh common.Shutdown, intervalSeconds int, maxRetries int) *RetrierImpl {
	return &RetrierImpl{
		conn:            conn,
		sh:              sh,
		ps:              util.NewPubSub[RetryEvent](),
		intervalSeconds: intervalSeconds,
		maxRetries:      maxRetries,
	}
}

func (r *RetrierImpl) Initialize() {
	go r.run()
}

func (r *RetrierImpl) ScheduleRetry(event db.EventPayload, lastResult string) error {
	error := r.conn.AddRetry(event, lastResult)
	if error != nil {
		return error
	}
	return nil
}

func (r *RetrierImpl) BumpRetry(id int, lastResult string) error {
	error := r.conn.BumpRetry(id, lastResult)
	if error != nil {
		return error
	}
	return nil
}

func (r *RetrierImpl) DeleteRetry(id int) error {
	error := r.conn.DeleteRetry(id)
	if error != nil {
		return error
	}
	return nil
}

func (r *RetrierImpl) Subscribe(cb func(ev RetryEvent)) int {
	return r.ps.Subscribe(cb)
}

func (r *RetrierImpl) run() {
	for {
		select {
		case <-r.sh.Ctx.Done():
			return
		case <-time.After(time.Duration(r.intervalSeconds) * time.Second):
		}

		retries, err := r.conn.GetRetriesTriedLessThan(r.maxRetries)

		if err != nil {
			blog.Error("Error getting retries", "error", err)
			continue
		}

		for _, retry := range retries {
			r.ps.PublishBlocking(RetryEvent{
				Retry: retry,
				Event: retry.EventPayload,
			})
		}

	}
}
