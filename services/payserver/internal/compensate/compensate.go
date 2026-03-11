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
	maxRetries   = 10
	batchSize    = 20
)

type Worker struct {
	store    *store.Store
	callback *notify.CallbackClient
	logger   *slog.Logger
	stop     chan struct{}
}

func NewWorker(st *store.Store, cb *notify.CallbackClient, logger *slog.Logger) *Worker {
	return &Worker{
		store:    st,
		callback: cb,
		logger:   logger,
		stop:     make(chan struct{}),
	}
}

func (w *Worker) Start() {
	go w.run()
}

func (w *Worker) Stop() {
	close(w.stop)
}

func (w *Worker) run() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.processPending()
		}
	}
}

func (w *Worker) processPending() {
	payments, err := w.store.ListPendingCallbacks(batchSize)
	if err != nil {
		w.logger.Error("compensate: list pending", "error", err)
		return
	}

	for _, p := range payments {
		if !w.shouldRetry(p) {
			continue
		}

		if p.CallbackRetries >= maxRetries {
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

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := w.callback.Send(ctx, payload)
		cancel()

		if err != nil {
			w.logger.Warn("compensate: callback failed", "order_id", p.OrderID, "retries", p.CallbackRetries, "error", err)
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
