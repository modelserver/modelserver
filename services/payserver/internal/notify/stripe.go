package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"

	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

// StripeNotifyHandler handles Stripe webhook events for checkout.session.completed.
type StripeNotifyHandler struct {
	webhookSecret string
	store         *store.Store
	callback      *CallbackClient
	logger        *slog.Logger
}

// NewStripeNotifyHandler creates a new StripeNotifyHandler.
func NewStripeNotifyHandler(secret string, st *store.Store, cb *CallbackClient, logger *slog.Logger) *StripeNotifyHandler {
	return &StripeNotifyHandler{
		webhookSecret: secret,
		store:         st,
		callback:      cb,
		logger:        logger,
	}
}

// ServeHTTP verifies the Stripe-Signature header, then handles
// checkout.session.completed events: marks the payment paid (phase 1) and
// calls the modelserver delivery endpoint (phase 2).
func (h *StripeNotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Phase 0: read raw body BEFORE any decoding — Stripe signature is over raw bytes.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get("Stripe-Signature")
	event, err := webhook.ConstructEventWithOptions(body, sig, h.webhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
	if err != nil {
		h.logger.Error("stripe notify: signature verification failed", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	// Only act on checkout.session.completed events; ack everything else.
	if event.Type != stripe.EventTypeCheckoutSessionCompleted {
		w.WriteHeader(http.StatusOK)
		return
	}

	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		h.logger.Error("stripe notify: decode session failed",
			"event_id", event.ID, "error", err)
		http.Error(w, "decode session", http.StatusBadRequest)
		return
	}

	// Ack if session is not yet paid (e.g. free or async payment pending).
	if sess.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		w.WriteHeader(http.StatusOK)
		return
	}

	orderID := uuidFromCompact(sess.ClientReferenceID)
	tradeNo := sess.ID
	paidAmount := sess.AmountTotal
	paidAt := time.Unix(event.Created, 0).UTC()

	payment, err := h.store.GetPaymentByOrderID(orderID)
	if err != nil || payment == nil {
		h.logger.Error("stripe notify: payment not found",
			"order_id", orderID, "channel", "stripe",
			"trade_no", tradeNo, "event_id", event.ID, "error", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if payment.Channel != "stripe" {
		h.logger.Error("stripe notify: channel mismatch",
			"order_id", orderID, "channel", payment.Channel,
			"trade_no", tradeNo, "event_id", event.ID)
		http.Error(w, "channel mismatch", http.StatusBadRequest)
		return
	}

	if paidAmount != payment.Amount {
		h.logger.Error("stripe notify: amount mismatch",
			"order_id", orderID, "channel", "stripe",
			"trade_no", tradeNo, "event_id", event.ID,
			"expected", payment.Amount, "got", paidAmount)
		http.Error(w, "amount mismatch", http.StatusBadRequest)
		return
	}

	// Idempotency: if already paid and callback succeeded, this is a duplicate webhook.
	if payment.Status == "paid" && payment.CallbackStatus == "success" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Phase 1: mark payment as paid (CAS on status='pending').
	if payment.Status == "pending" {
		rawNotify, _ := json.Marshal(sess)
		updated, err := h.store.MarkPaymentPaid(payment.TenantID, orderID, tradeNo, string(rawNotify), paidAt)
		if err != nil {
			h.logger.Error("stripe notify: mark paid failed",
				"order_id", orderID, "channel", "stripe",
				"trade_no", tradeNo, "event_id", event.ID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !updated {
			// Concurrent webhook lost the CAS race; the row already transitioned
			// out of pending. The idempotency guard above catches the
			// paid+success case; this log surfaces the rarer paid+pending race.
			h.logger.Warn("stripe notify: payment already transitioned from pending",
				"order_id", orderID, "channel", "stripe",
				"trade_no", tradeNo, "event_id", event.ID)
		}
	}

	// Ack Stripe before calling modelserver.
	w.WriteHeader(http.StatusOK)

	// Phase 2: resolve tenant + deliver callback.
	t, err := h.store.GetTenantByID(payment.TenantID)
	if err != nil {
		h.logger.Error("stripe notify: tenant lookup failed",
			"order_id", orderID, "tenant_id", payment.TenantID,
			"event_id", event.ID, "error", err)
		// Don't IncrCallbackRetries — DB error is transient, the
		// compensate worker will retry the lookup on its next pass.
		return
	}
	if t == nil || !t.IsActive {
		h.logger.Warn("stripe notify: tenant missing or inactive; skipping callback",
			"order_id", orderID, "tenant_id", payment.TenantID, "event_id", event.ID)
		// Mark failed: a deleted/disabled tenant will never accept callbacks.
		// Compensate worker should not retry forever.
		if err := h.store.MarkCallbackFailed(payment.TenantID, orderID); err != nil {
			h.logger.Warn("stripe notify: mark callback failed", "order_id", orderID, "err", err)
		}
		return
	}

	target := CallbackTarget{URL: t.CallbackURL, Secret: t.CallbackSecret}
	payload := DeliveryPayload{
		OrderID:    orderID,
		PaymentRef: payment.ID,
		Status:     "paid",
		PaidAmount: payment.Amount,
		PaidAt:     paidAt.Format(time.RFC3339),
	}
	cbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.callback.Send(cbCtx, target, payload); err != nil {
		h.logger.Warn("stripe notify: callback failed, will retry",
			"order_id", orderID, "tenant_id", t.ID, "event_id", event.ID, "error", err)
		h.store.IncrCallbackRetries(payment.TenantID, orderID)
		return
	}
	h.store.MarkCallbackSuccess(payment.TenantID, orderID)
}
