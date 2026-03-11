package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type paymentAPIRequest struct {
	OrderID     string            `json:"order_id"`
	ProductName string            `json:"product_name"`
	Channel     string            `json:"channel"`
	Currency    string            `json:"currency"`
	Amount      int64             `json:"amount"`
	NotifyURL   string            `json:"notify_url"`
	ReturnURL   string            `json:"return_url"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type paymentAPIResponse struct {
	PaymentRef string `json:"payment_ref"`
	PaymentURL string `json:"payment_url"`
	Status     string `json:"status"`
}

func handleCreatePayment(st *store.Store, gateways map[string]gateway.Gateway, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req paymentAPIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		if req.OrderID == "" || req.Channel == "" || req.Amount <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "order_id, channel, and amount are required"})
			return
		}

		gw, ok := gateways[req.Channel]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported channel: " + req.Channel})
			return
		}

		// Idempotency: check if payment already exists for this order_id
		existing, err := st.GetPaymentByOrderID(req.OrderID)
		if err != nil {
			logger.Error("check existing payment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if existing != nil {
			if existing.Status == "paid" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "order already paid"})
				return
			}
			// Return existing pending payment
			writeJSON(w, http.StatusOK, paymentAPIResponse{
				PaymentRef: existing.ID,
				PaymentURL: existing.PaymentURL,
				Status:     "pending",
			})
			return
		}

		// Call payment gateway
		result, err := gw.CreatePayment(r.Context(), &gateway.PaymentRequest{
			OutTradeNo:  req.OrderID,
			Description: req.ProductName,
			Amount:      req.Amount,
			NotifyURL:   req.NotifyURL,
			ReturnURL:   req.ReturnURL,
		})
		if err != nil {
			logger.Error("create payment", "channel", req.Channel, "order_id", req.OrderID, "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment gateway error"})
			return
		}

		// Persist payment record
		payment := &store.Payment{
			OrderID:    req.OrderID,
			Channel:    req.Channel,
			TradeNo:    result.TradeNo,
			PaymentURL: result.PaymentURL,
			Amount:     req.Amount,
			Status:     "pending",
		}
		if err := st.CreatePayment(payment); err != nil {
			logger.Error("persist payment", "order_id", req.OrderID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create payment record"})
			return
		}

		writeJSON(w, http.StatusOK, paymentAPIResponse{
			PaymentRef: payment.ID,
			PaymentURL: result.PaymentURL,
			Status:     "pending",
		})
	}
}

func bearerAuthMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || auth[7:] != apiKey {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
