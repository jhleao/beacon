package dispatcher

import (
	"context"
	"time"
)

const (
	// HeartbeatInterval is how often workers update their heartbeat.
	HeartbeatInterval = 10 * time.Second

	// StaleThreshold is how long before a worker is considered stale.
	StaleThreshold = 30 * time.Second

	// ReaperInterval is how often the reaper checks for stale workers.
	ReaperInterval = 15 * time.Second
)

func (d *Dispatcher) registerHeartbeat(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO beacon.worker_heartbeats (worker_id, last_heartbeat, started_at)
		VALUES ($1, now(), now())
		ON CONFLICT (worker_id) DO UPDATE
		SET last_heartbeat = now()
	`, d.workerID)
	return err
}

func (d *Dispatcher) heartbeatLoop(ctx context.Context) {
	defer d.wg.Done()

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.deregisterHeartbeat(context.Background()) // Use fresh context
			return
		case <-d.stopCh:
			d.deregisterHeartbeat(context.Background())
			return
		case <-ticker.C:
			if err := d.updateHeartbeat(ctx); err != nil {
				d.logger.Error("failed to update heartbeat", "error", err)
			}
		}
	}
}

func (d *Dispatcher) updateHeartbeat(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE beacon.worker_heartbeats
		SET last_heartbeat = now()
		WHERE worker_id = $1
	`, d.workerID)
	return err
}

func (d *Dispatcher) deregisterHeartbeat(ctx context.Context) {
	_, err := d.pool.Exec(ctx, `
		DELETE FROM beacon.worker_heartbeats WHERE worker_id = $1
	`, d.workerID)
	if err != nil {
		d.logger.Error("failed to deregister heartbeat", "error", err)
	}
}
