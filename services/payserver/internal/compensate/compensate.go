package compensate

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

const (
	pollInterval = 30 * time.Second
	MaxRetries   = 10
	batchSize    = 20
)

type Worker struct {
	store    *store.Store
	callback *notify.CallbackClient
	logger   *slog.Logger
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewWorker(st *store.Store, cb *notify.CallbackClient, logger *slog.Logger) *Worker {
	return &Worker{
		store:    st,
		callback: cb,
		logger:   logger,
		done:     make(chan struct{}),
	}
}

func (w *Worker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.run(ctx)
}

// Stop signals the worker to stop and waits for it to finish.
func (w *Worker) Stop() {
	w.cancel()
	<-w.done
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processPending(ctx)
		}
	}
}

func (w *Worker) processPending(ctx context.Context) {
	payments, err := w.store.ListPendingCallbacks(batchSize, MaxRetries)
	if err != nil {
		w.logger.Error("compensate: list pending", "error", err)
		return
	}

	for _, p := range payments {
		// Check if shutdown was requested.
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !w.shouldRetry(p) {
			continue
		}

		if p.CallbackRetries >= MaxRetries {
			w.logger.Error("compensate: max retries reached", "order_id", p.OrderID)
			w.store.MarkCallbackFailed(p.OrderID)
			continue
		}

		payload := notify.DeliveryPayload{
			OrderID:    p.OrderID,
			PaymentRef: p.ID,
			Status:     "paid",
			PaidAmount: p.Amount,
		}
		if p.PaidAt != nil {
			payload.PaidAt = p.PaidAt.Format(time.RFC3339)
		}

		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := w.callback.Send(callCtx, payload)
		cancel()

		if err != nil {
			w.logger.Warn("compensate: callback failed",
				"order_id", p.OrderID, "retries", p.CallbackRetries, "error", err)
			w.store.IncrCallbackRetries(p.OrderID)
			continue
		}

		w.logger.Info("compensate: callback succeeded", "order_id", p.OrderID)
		w.store.MarkCallbackSuccess(p.OrderID)
	}
}

func (w *Worker) shouldRetry(p store.Payment) bool {
	wait := backoffDuration(p.CallbackRetries)
	return time.Since(p.UpdatedAt) >= wait
}

// backoffDuration returns exponential backoff: 30s * 2^retries.
func backoffDuration(retries int) time.Duration {
	d := pollInterval
	for i := 0; i < retries; i++ {
		d *= 2
	}
	return d
}
