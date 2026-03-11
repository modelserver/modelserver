package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"

	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type WeChatNotifyHandler struct {
	notifyHandler *notify.Handler
	store         *store.Store
	callback      *CallbackClient
	logger        *slog.Logger
}

func NewWeChatNotifyHandler(handler *notify.Handler, st *store.Store, cb *CallbackClient, logger *slog.Logger) *WeChatNotifyHandler {
	return &WeChatNotifyHandler{
		notifyHandler: handler,
		store:         st,
		callback:      cb,
		logger:        logger,
	}
}

func NewWeChatNotifyHandlerFromVerifier(apiV3Key string, certVisitor core.CertificateVisitor, st *store.Store, cb *CallbackClient, logger *slog.Logger) *WeChatNotifyHandler {
	handler := notify.NewNotifyHandler(apiV3Key, verifiers.NewSHA256WithRSAVerifier(certVisitor))
	return NewWeChatNotifyHandler(handler, st, cb, logger)
}

func (h *WeChatNotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var tx payments.Transaction
	_, err := h.notifyHandler.ParseNotifyRequest(r.Context(), r, &tx)
	if err != nil {
		h.logger.Error("wechat notify: parse failed", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"code": "FAIL", "message": err.Error()})
		return
	}

	orderID := *tx.OutTradeNo
	tradeNo := *tx.TransactionId
	gatewayAmount := *tx.Amount.Total
	paidAt := time.Now()
	if tx.SuccessTime != nil {
		if parsed, parseErr := time.Parse(time.RFC3339, *tx.SuccessTime); parseErr == nil {
			paidAt = parsed
		}
	}

	// Idempotency: check payment status
	payment, err := h.store.GetPaymentByOrderID(orderID)
	if err != nil || payment == nil {
		h.logger.Error("wechat notify: payment not found", "order_id", orderID, "error", err)
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"code": "FAIL", "message": "payment not found"})
		return
	}

	// Verify amount matches what we expect.
	if gatewayAmount != payment.Amount {
		h.logger.Error("wechat notify: amount mismatch",
			"order_id", orderID, "expected", payment.Amount, "got", gatewayAmount)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"code": "FAIL", "message": "amount mismatch"})
		return
	}

	if payment.Status == "paid" && payment.CallbackStatus == "success" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"code": "SUCCESS", "message": "OK"})
		return
	}

	// Phase 1: mark as paid (if not already)
	if payment.Status == "pending" {
		rawNotify, _ := json.Marshal(tx)
		updated, err := h.store.MarkPaymentPaid(orderID, tradeNo, string(rawNotify), paidAt)
		if err != nil {
			h.logger.Error("wechat notify: mark paid failed", "order_id", orderID, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"code": "FAIL", "message": "internal error"})
			return
		}
		if !updated {
			h.logger.Warn("wechat notify: payment already transitioned from pending", "order_id", orderID)
		}
	}

	// Reply success to WeChat immediately
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"code": "SUCCESS", "message": "OK"})

	// Phase 2: callback modelserver (best-effort, compensated if fails).
	// Use a detached context since we already replied to WeChat.
	payload := DeliveryPayload{
		OrderID:    orderID,
		PaymentRef: payment.ID,
		Status:     "paid",
		PaidAmount: payment.Amount, // Use DB-authoritative amount, not gateway amount
		PaidAt:     paidAt.Format(time.RFC3339),
	}

	cbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.callback.Send(cbCtx, payload); err != nil {
		h.logger.Warn("wechat notify: callback to modelserver failed, will retry",
			"order_id", orderID, "error", err)
		h.store.IncrCallbackRetries(orderID)
		return
	}
	h.store.MarkCallbackSuccess(orderID)
}
