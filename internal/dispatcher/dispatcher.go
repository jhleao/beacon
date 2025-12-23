// Package dispatcher manages event delivery with worker pools, heartbeats, and crash recovery.
package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"beacon/internal/dispatcher/retry"
	"beacon/internal/httpdeliver"
	"beacon/internal/outbox"

	"github.com/google/uuid"
)

// Config holds dispatcher configuration.
type Config struct {
	PollInterval time.Duration
	BatchSize    int
	WorkerCount  int
}

// Dispatcher manages event claiming and delivery.
type Dispatcher struct {
	pool       poolInterface
	repo       *outbox.Repository
	client     *httpdeliver.Client
	config     Config
	workerID   string
	logger     *slog.Logger
	semaphores *Semaphores

	wg       sync.WaitGroup
	eventsCh chan outbox.ClaimedEvent
	stopOnce sync.Once
	stopCh   chan struct{}
}

// poolInterface abstracts db.Pool for testing.
type poolInterface interface {
	Exec(ctx context.Context, sql string, args ...any) (any, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// Row abstracts pgx.Row for testing.
type Row interface {
	Scan(dest ...any) error
}

// New creates a new Dispatcher.
func New(
	pool poolInterface,
	repo *outbox.Repository,
	client *httpdeliver.Client,
	config Config,
	logger *slog.Logger,
) *Dispatcher {
	// Generate unique worker ID
	workerID := fmt.Sprintf("beacon-%s-%d", uuid.New().String()[:8], time.Now().UnixNano()%1000)

	return &Dispatcher{
		pool:       pool,
		repo:       repo,
		client:     client,
		config:     config,
		workerID:   workerID,
		logger:     logger.With("worker_id", workerID),
		semaphores: NewSemaphores(),
		eventsCh:   make(chan outbox.ClaimedEvent, config.BatchSize),
		stopCh:     make(chan struct{}),
	}
}

// Start begins the dispatcher loops.
func (d *Dispatcher) Start(ctx context.Context) error {
	d.logger.Info("starting dispatcher",
		"poll_interval", d.config.PollInterval,
		"batch_size", d.config.BatchSize,
		"worker_count", d.config.WorkerCount,
	)

	// Register heartbeat
	if err := d.registerHeartbeat(ctx); err != nil {
		return fmt.Errorf("register heartbeat: %w", err)
	}

	// Start worker goroutines
	for i := 0; i < d.config.WorkerCount; i++ {
		d.wg.Add(1)
		go d.worker(ctx, i)
	}

	// Start heartbeat loop
	d.wg.Add(1)
	go d.heartbeatLoop(ctx)

	// Start reaper loop
	d.wg.Add(1)
	go d.reaperLoop(ctx)

	// Start polling loop (blocking)
	d.pollLoop(ctx)

	return nil
}

// Stop gracefully stops the dispatcher.
func (d *Dispatcher) Stop() {
	d.stopOnce.Do(func() {
		d.logger.Info("stopping dispatcher")
		close(d.stopCh)
		close(d.eventsCh)
	})
}

// Wait waits for all workers to finish.
func (d *Dispatcher) Wait() {
	d.wg.Wait()
	d.logger.Info("dispatcher stopped")
}

// WorkerID returns the dispatcher's worker ID.
func (d *Dispatcher) WorkerID() string {
	return d.workerID
}

func (d *Dispatcher) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(d.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.poll(ctx)
		}
	}
}

func (d *Dispatcher) poll(ctx context.Context) {
	events, err := d.repo.Claim(ctx, d.workerID, d.config.BatchSize)
	if err != nil {
		d.logger.Error("failed to claim events", "error", err)
		return
	}

	if len(events) > 0 {
		d.logger.Debug("claimed events", "count", len(events))
	}

	for _, event := range events {
		select {
		case d.eventsCh <- event:
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		}
	}
}

func (d *Dispatcher) worker(ctx context.Context, id int) {
	defer d.wg.Done()

	workerLogger := d.logger.With("worker_num", id)
	workerLogger.Debug("worker started")

	for {
		select {
		case <-ctx.Done():
			workerLogger.Debug("worker stopping (context done)")
			return
		case <-d.stopCh:
			workerLogger.Debug("worker stopping (stop signal)")
			return
		case event, ok := <-d.eventsCh:
			if !ok {
				workerLogger.Debug("worker stopping (channel closed)")
				return
			}
			d.processEvent(ctx, workerLogger, event)
		}
	}
}

func (d *Dispatcher) processEvent(ctx context.Context, logger *slog.Logger, claimed outbox.ClaimedEvent) {
	event := claimed.Event
	dest := claimed.Destination

	eventLogger := logger.With(
		"event_id", event.ID,
		"subscription_id", event.SubscriptionID,
		"destination", dest.Name,
		"attempt", event.Attempts,
	)

	// Acquire per-destination semaphore
	sem := d.semaphores.Get(dest.ID, dest.MaxInFlight)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-sem }()

	// Deliver
	startedAt := time.Now()
	statusCode, respHeaders, err := d.client.Deliver(ctx, dest, event)
	finishedAt := time.Now()
	duration := finishedAt.Sub(startedAt)

	// Record attempt for audit
	attempt := outbox.DeliveryAttempt{
		EventID:         event.ID,
		DestinationID:   dest.ID,
		Attempt:         event.Attempts,
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		StatusCode:      statusCode,
		ResponseHeaders: respHeaders,
	}
	if err != nil {
		errStr := err.Error()
		attempt.Error = &errStr
	}

	if recordErr := d.repo.RecordAttempt(ctx, attempt); recordErr != nil {
		eventLogger.Warn("failed to record attempt", "error", recordErr)
	}

	// Handle result
	if err != nil {
		d.handleError(ctx, eventLogger, event, err, duration)
		return
	}

	if *statusCode >= 200 && *statusCode < 300 {
		// Success
		if ackErr := d.repo.Ack(ctx, event.ID); ackErr != nil {
			eventLogger.Error("failed to ack event", "error", ackErr)
			return
		}
		eventLogger.Info("event delivered",
			"status_code", *statusCode,
			"duration_ms", duration.Milliseconds(),
		)
		return
	}

	// Non-2xx response
	d.handleHTTPError(ctx, eventLogger, event, *statusCode, duration)
}

func (d *Dispatcher) handleError(ctx context.Context, logger *slog.Logger, event outbox.Event, err error, duration time.Duration) {
	errMsg := err.Error()

	if retry.IsRetryableError(err) && retry.ShouldRetry(event.Attempts) {
		nextDelay := retry.NextDelay(event.Attempts)
		visibleAt := time.Now().Add(nextDelay)

		if reschedErr := d.repo.Reschedule(ctx, event.ID, visibleAt, errMsg); reschedErr != nil {
			logger.Error("failed to reschedule event", "error", reschedErr)
			return
		}

		logger.Warn("event rescheduled",
			"error", errMsg,
			"next_delay", nextDelay,
			"duration_ms", duration.Milliseconds(),
		)
		return
	}

	// Dead-letter
	if deadErr := d.repo.ToDead(ctx, event.ID, errMsg); deadErr != nil {
		logger.Error("failed to dead-letter event", "error", deadErr)
		return
	}

	logger.Error("event dead-lettered",
		"reason", errMsg,
		"attempts", event.Attempts,
	)
}

func (d *Dispatcher) handleHTTPError(ctx context.Context, logger *slog.Logger, event outbox.Event, statusCode int, duration time.Duration) {
	errMsg := fmt.Sprintf("HTTP %d", statusCode)

	if retry.IsPermanentFailure(statusCode) {
		// Permanent failure (4xx) - dead-letter immediately
		if deadErr := d.repo.ToDead(ctx, event.ID, errMsg); deadErr != nil {
			logger.Error("failed to dead-letter event", "error", deadErr)
			return
		}
		logger.Error("event dead-lettered",
			"status_code", statusCode,
			"reason", "permanent failure",
		)
		return
	}

	if retry.IsRetryableStatus(statusCode) && retry.ShouldRetry(event.Attempts) {
		nextDelay := retry.NextDelay(event.Attempts)
		visibleAt := time.Now().Add(nextDelay)

		if reschedErr := d.repo.Reschedule(ctx, event.ID, visibleAt, errMsg); reschedErr != nil {
			logger.Error("failed to reschedule event", "error", reschedErr)
			return
		}

		logger.Warn("event rescheduled",
			"status_code", statusCode,
			"next_delay", nextDelay,
			"duration_ms", duration.Milliseconds(),
		)
		return
	}

	// Max attempts or non-retryable status
	if deadErr := d.repo.ToDead(ctx, event.ID, errMsg); deadErr != nil {
		logger.Error("failed to dead-letter event", "error", deadErr)
		return
	}

	logger.Error("event dead-lettered",
		"status_code", statusCode,
		"attempts", event.Attempts,
	)
}
