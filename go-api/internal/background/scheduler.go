package background

import (
	"context"
	"errors"
	"time"

	"forest/go-api/internal/queue"
)

const defaultInterval = time.Minute

const statRefreshQueueName = "stat_refresh"

type Heartbeater interface {
	TouchScheduleHeartbeat(ctx context.Context) error
}

type OrderProcessor interface {
	HandlePendingOrders(ctx context.Context) error
}

type StatProcessor interface {
	RefreshLegacyStats(ctx context.Context) error
}

type Runner struct {
	jobs      queue.Enqueuer
	heartbeat Heartbeater
	orders    OrderProcessor
	stats     StatProcessor
	interval  time.Duration
}

func NewRunner(jobs queue.Enqueuer, heartbeat Heartbeater, orders OrderProcessor, stats StatProcessor) *Runner {
	return &Runner{
		jobs:      jobs,
		heartbeat: heartbeat,
		orders:    orders,
		stats:     stats,
		interval:  defaultInterval,
	}
}

func (r *Runner) Start(ctx context.Context) {
	if r == nil {
		return
	}

	go func() {
		_ = r.RunOnce(ctx)

		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = r.RunOnce(ctx)
			}
		}
	}()
}

func (r *Runner) RunOnce(ctx context.Context) error {
	if r == nil {
		return nil
	}

	var errs []error
	if r.heartbeat != nil {
		if err := r.heartbeat.TouchScheduleHeartbeat(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if r.orders != nil && !r.queueBusy("order_handle") {
		if err := r.jobs.Enqueue("order_handle", "order-handle:sweep", func(jobCtx context.Context) error {
			return r.orders.HandlePendingOrders(jobCtx)
		}); err != nil {
			errs = append(errs, err)
		}
	}

	if r.stats != nil && !r.queueBusy(statRefreshQueueName) {
		if err := r.jobs.Enqueue(statRefreshQueueName, "stat:refresh", func(jobCtx context.Context) error {
			return r.stats.RefreshLegacyStats(jobCtx)
		}); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (r *Runner) queueBusy(queueName string) bool {
	if r == nil || r.jobs == nil {
		return false
	}

	for _, item := range r.jobs.Snapshot().Queues {
		if item.Name == queueName && (item.Length > 0 || item.Processes > 0) {
			return true
		}
	}
	return false
}
