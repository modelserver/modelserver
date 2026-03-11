package server

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

const maxRequestBodySize = 64 * 1024 // 64 KB

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
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

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
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported channel"})
			return
		}

		// Insert-first pattern: atomically insert a placeholder record or retrieve existing.
		// This prevents TOCTOU races where concurrent requests could both call the gateway.
		payment := &store.Payment{
			OrderID: req.OrderID,
			Channel: req.Channel,
			Amount:  req.Amount,
			Status:  "pending",
		}
		created, err := st.InsertOrGetPayment(payment)
		if err != nil {
			logger.Error("insert or get payment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		if !created {
			// Existing record — idempotency handling.
			if payment.Status == "paid" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "order already paid"})
				return
			}
			// Return existing pending payment.
			writeJSON(w, http.StatusOK, paymentAPIResponse{
				PaymentRef: payment.ID,
				PaymentURL: payment.PaymentURL,
				Status:     "pending",
			})
			return
		}

		// New record inserted — call payment gateway.
		result, err := gw.CreatePayment(r.Context(), &gateway.PaymentRequest{
			OutTradeNo:  req.OrderID,
			Description: req.ProductName,
			Amount:      req.Amount,
		})
		if err != nil {
			logger.Error("create payment", "channel", req.Channel, "order_id", req.OrderID, "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment gateway error"})
			return
		}

		// Update the record with gateway result.
		if err := st.UpdatePaymentGatewayResult(payment.ID, result.TradeNo, result.PaymentURL); err != nil {
			logger.Error("update payment gateway result", "order_id", req.OrderID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update payment record"})
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
	apiKeyBytes := []byte(apiKey)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			token := []byte(auth[7:])
			if subtle.ConstantTimeCompare(token, apiKeyBytes) != 1 {
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
