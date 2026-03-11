package notify

import (
	"encoding/json"
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

	if err := h.gateway.VerifyCallback(r.Form); err != nil {
		h.logger.Error("alipay notify: signature verification failed", "error", err)
		http.Error(w, "fail", http.StatusUnauthorized)
		return
	}

	tradeStatus := r.FormValue("trade_status")
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	orderID := r.FormValue("out_trade_no")
	tradeNo := r.FormValue("trade_no")
	totalAmountStr := r.FormValue("total_amount")
	paidAmount := parseYuanToFen(totalAmountStr)
	paidAt := time.Now()

	payment, err := h.store.GetPaymentByOrderID(orderID)
	if err != nil || payment == nil {
		h.logger.Error("alipay notify: payment not found", "order_id", orderID, "error", err)
		http.Error(w, "fail", http.StatusNotFound)
		return
	}

	if payment.Status == "paid" && payment.CallbackStatus == "success" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	// Phase 1: mark as paid
	if payment.Status == "pending" {
		rawNotify, _ := json.Marshal(r.Form)
		if err := h.store.MarkPaymentPaid(orderID, tradeNo, string(rawNotify), paidAt); err != nil {
			h.logger.Error("alipay notify: mark paid failed", "order_id", orderID, "error", err)
			http.Error(w, "fail", http.StatusInternalServerError)
			return
		}
	}

	// Reply success to Alipay immediately
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("success"))

	// Phase 2: callback modelserver
	payload := DeliveryPayload{
		OrderID:    orderID,
		PaymentRef: payment.ID,
		Status:     "paid",
		PaidAmount: paidAmount,
		PaidAt:     paidAt.Format(time.RFC3339),
	}

	if err := h.callback.Send(r.Context(), payload); err != nil {
		h.logger.Warn("alipay notify: callback to modelserver failed, will retry", "order_id", orderID, "error", err)
		h.store.IncrCallbackRetries(orderID)
		return
	}
	h.store.MarkCallbackSuccess(orderID)
}

// parseYuanToFen converts "20.00" to 2000.
func parseYuanToFen(yuan string) int64 {
	var result int64
	var decimal int64
	var decimalPlaces int
	inDecimal := false

	for _, c := range yuan {
		if c == '.' {
			inDecimal = true
			continue
		}
		if c >= '0' && c <= '9' {
			if inDecimal {
				if decimalPlaces < 2 {
					decimal = decimal*10 + int64(c-'0')
					decimalPlaces++
				}
			} else {
				result = result*10 + int64(c-'0')
			}
		}
	}
	for decimalPlaces < 2 {
		decimal *= 10
		decimalPlaces++
	}
	return result*100 + decimal
}
