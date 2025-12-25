package dispatcher

import (
	"context"
	"time"
)

func (d *Dispatcher) reaperLoop(ctx context.Context) {
	defer d.wg.Done()

	ticker := time.NewTicker(ReaperInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.reapStaleWorkers(ctx)
		}
	}
}

func (d *Dispatcher) reapStaleWorkers(ctx context.Context) {
	var staleCount int64
	err := d.pool.QueryRow(ctx, `
		WITH stale AS (
			SELECT worker_id FROM beacon.worker_heartbeats
			WHERE last_heartbeat < now() - $1::interval
		),
		reclaimed AS (
			UPDATE beacon.outbox_events
			SET
				state = 'pending',
				locked_by = NULL,
				locked_at = NULL,
				visible_at = now()
			WHERE locked_by IN (SELECT worker_id FROM stale)
			  AND state = 'delivering'
			RETURNING 1
		),
		deleted AS (
			DELETE FROM beacon.worker_heartbeats
			WHERE worker_id IN (SELECT worker_id FROM stale)
		)
		SELECT COUNT(*) FROM reclaimed
	`, StaleThreshold.String()).Scan(&staleCount)

	if err != nil {
		d.logger.Error("failed to reap stale workers", "error", err)
		return
	}

	if staleCount > 0 {
		d.logger.Info("reclaimed stale events", "count", staleCount)
	}
}
