package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type AlipayNotifyHandler struct {
	gateway  *gateway.AlipayGateway
	store    *store.Store
	callback *CallbackClient
	logger   *slog.Logger
}

func NewAlipayNotifyHandler(gw *gateway.AlipayGateway, st *store.Store, cb *CallbackClient, logger *slog.Logger) *AlipayNotifyHandler {
	return &AlipayNotifyHandler{
		gateway:  gw,
		store:    st,
		callback: cb,
		logger:   logger,
	}
}

func (h *AlipayNotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.logger.Error("alipay notify: parse form failed", "error", err)
		http.Error(w, "fail", http.StatusBadRequest)
		return
	}

	// Use r.PostForm (POST body only), not r.Form (which includes URL query params).
	if err := h.gateway.VerifyCallback(r.PostForm); err != nil {
		h.logger.Error("alipay notify: signature verification failed", "error", err)
		http.Error(w, "fail", http.StatusUnauthorized)
		return
	}

	tradeStatus := r.PostFormValue("trade_status")
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	orderID := uuidFromCompact(r.PostFormValue("out_trade_no"))
	tradeNo := r.PostFormValue("trade_no")
	totalAmountStr := r.PostFormValue("total_amount")
	paidAmount, err := parseYuanToFen(totalAmountStr)
	if err != nil {
		h.logger.Error("alipay notify: invalid total_amount", "value", totalAmountStr, "error", err)
		http.Error(w, "fail", http.StatusBadRequest)
		return
	}

	// Parse Alipay's actual payment time.
	paidAt := time.Now()
	if gmtPayment := r.PostFormValue("gmt_payment"); gmtPayment != "" {
		if t, parseErr := time.ParseInLocation("2006-01-02 15:04:05", gmtPayment, time.Local); parseErr == nil {
			paidAt = t
		}
	}

	payment, err := h.store.GetPaymentByOrderID(orderID)
	if err != nil || payment == nil {
		h.logger.Error("alipay notify: payment not found", "order_id", orderID, "error", err)
		http.Error(w, "fail", http.StatusNotFound)
		return
	}

	// Verify amount matches what we expect.
	if paidAmount != payment.Amount {
		h.logger.Error("alipay notify: amount mismatch",
			"order_id", orderID, "expected", payment.Amount, "got", paidAmount)
		http.Error(w, "fail", http.StatusBadRequest)
		return
	}

	if payment.Status == "paid" && payment.CallbackStatus == "success" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	// Phase 1: mark as paid
	if payment.Status == "pending" {
		rawNotify, _ := json.Marshal(r.PostForm)
		updated, err := h.store.MarkPaymentPaid(orderID, tradeNo, string(rawNotify), paidAt)
		if err != nil {
			h.logger.Error("alipay notify: mark paid failed", "order_id", orderID, "error", err)
			http.Error(w, "fail", http.StatusInternalServerError)
			return
		}
		if !updated {
			h.logger.Warn("alipay notify: payment already transitioned from pending", "order_id", orderID)
		}
	}

	// Reply success to Alipay immediately
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("success"))

	// Phase 2: callback modelserver (detached context since we already replied to Alipay).
	payload := DeliveryPayload{
		OrderID:    orderID,
		PaymentRef: payment.ID,
		Status:     "paid",
		PaidAmount: payment.Amount, // Use DB-authoritative amount
		PaidAt:     paidAt.Format(time.RFC3339),
	}

	cbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.callback.Send(cbCtx, payload); err != nil {
		h.logger.Warn("alipay notify: callback to modelserver failed, will retry",
			"order_id", orderID, "error", err)
		h.store.IncrCallbackRetries(orderID)
		return
	}
	h.store.MarkCallbackSuccess(orderID)
}

// parseYuanToFen converts "20.00" to 2000. Returns an error on empty or invalid input.
func parseYuanToFen(yuan string) (int64, error) {
	if yuan == "" {
		return 0, fmt.Errorf("empty amount")
	}

	var result int64
	var decimal int64
	var decimalPlaces int
	inDecimal := false
	hasDigit := false

	for _, c := range yuan {
		if c == '.' {
			if inDecimal {
				return 0, fmt.Errorf("multiple decimal points in %q", yuan)
			}
			inDecimal = true
			continue
		}
		if c >= '0' && c <= '9' {
			hasDigit = true
			if inDecimal {
				if decimalPlaces < 2 {
					decimal = decimal*10 + int64(c-'0')
					decimalPlaces++
				}
			} else {
				result = result*10 + int64(c-'0')
			}
		} else {
			return 0, fmt.Errorf("invalid character %q in amount %q", string(c), yuan)
		}
	}
	if !hasDigit {
		return 0, fmt.Errorf("no digits in amount %q", yuan)
	}
	for decimalPlaces < 2 {
		decimal *= 10
		decimalPlaces++
	}
	return result*100 + decimal, nil
}
