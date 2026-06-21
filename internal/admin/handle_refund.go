package admin

import (
	"log/slog"
	"net/http"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// handleBillingRefundWebhook applies a payserver-delivered refund event
// against the originating order. Mounted behind HMACAuthMiddleware so
// only payserver-signed requests reach this handler.
//
// Body shape: {order_id, amount, currency}
// NOTE: the actual payserver refund payload shape is TBD. This implements
// the brief-specified shape; a one-field Marshal update will reconcile if
// the real payserver sends differently-named fields.
//
// Defensive gates (security-review-driven):
//
//  1. Partial refunds are NOT supported in V1. The MVP refund policy is
//     full reversal of the original topup (spec §5 / B1). If a webhook
//     arrives with `amount != order.Amount` (or `currency != order.Currency`)
//     we refuse to silently apply a full reversal — that would either
//     over-refund credits (lose platform money) or under-refund credits
//     (under-charge the user). Returns 422 so the upstream webhook
//     keeps retrying and ops are alerted; once partial-refund support
//     ships (V2) the gate inverts.
//
//  2. Status gate. Refunds only apply to orders that actually delivered
//     credits to the wallet. Pending / paying / failed / cancelled orders
//     are not refundable (nothing to reverse). Already-refunded orders
//     short-circuit to 200 idempotent — the store-layer
//     `uniq_eut_refund_order` partial unique index is the authoritative
//     idempotency guard; the status check is defense in depth.
//
//  3. On success the order transitions to OrderStatusRefunded as a
//     terminal state. Failure to update the status after the ledger row
//     lands is logged but doesn't fail the response — the ledger row is
//     the source of truth for "money moved", the status is a status flag.
func handleBillingRefundWebhook(st *store.Store, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OrderID  string `json:"order_id"`
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
		if body.OrderID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "order_id required")
			return
		}

		order, err := st.GetOrderByID(body.OrderID)
		if err != nil || order == nil {
			writeError(w, http.StatusNotFound, "not_found", "order not found")
			return
		}

		switch order.OrderType {
		case types.OrderTypeExtraUsageTopup:
			// Status idempotency: already-refunded orders return cleanly
			// without re-mutating the ledger.
			if order.Status == types.OrderStatusRefunded {
				logger.Info("refund webhook for already-refunded order",
					"order_id", body.OrderID)
				writeData(w, http.StatusOK, map[string]any{
					"order_id": body.OrderID,
					"status":   "already_refunded",
				})
				return
			}
			// Status gate: only delivered orders are refundable.
			if order.Status != types.OrderStatusDelivered {
				logger.Warn("refund webhook for non-delivered order",
					"order_id", body.OrderID,
					"status", order.Status)
				writeError(w, http.StatusConflict, "not_refundable",
					"order cannot be refunded in its current state")
				return
			}
			// Amount/currency parity gate: V1 only supports full reversal.
			if body.Amount != order.Amount || body.Currency != order.Currency {
				logger.Error("partial or mismatched refund webhook — not supported in V1",
					"order_id", body.OrderID,
					"event_amount", body.Amount, "order_amount", order.Amount,
					"event_currency", body.Currency, "order_currency", order.Currency)
				writeError(w, http.StatusUnprocessableEntity, "partial_refund_unsupported",
					"refund amount/currency must match the original order exactly; "+
						"partial refunds are not supported in this version")
				return
			}

			newBal, err := st.RefundExtraUsageTopup(body.OrderID)
			if err != nil {
				logger.Error("refund failed", "order_id", body.OrderID, "err", err)
				writeError(w, http.StatusInternalServerError, "internal", "refund failed")
				return
			}
			// Best-effort status transition. The ledger row is already in
			// place (and the unique index makes RefundExtraUsageTopup
			// idempotent), so a stale status here is recoverable —
			// retry of the same webhook will hit the OrderStatusRefunded
			// short-circuit above or re-attempt this transition.
			if updErr := st.UpdateOrderStatus(body.OrderID, types.OrderStatusRefunded); updErr != nil {
				logger.Error("refund applied but order status update failed",
					"order_id", body.OrderID, "err", updErr)
			}
			logger.Info("refund applied",
				"order_id", body.OrderID,
				"new_balance_credits", newBal)
			writeData(w, http.StatusOK, map[string]any{
				"order_id":            body.OrderID,
				"new_balance_credits": newBal,
			})

		case types.OrderTypeSubscription:
			// Subscription refunds: out of scope for this PR; no-op with
			// observability so ops can investigate manually.
			logger.Warn("subscription refund received but unhandled",
				"order_id", body.OrderID)
			writeData(w, http.StatusAccepted, map[string]string{"status": "unhandled"})

		default:
			writeError(w, http.StatusBadRequest, "bad_request",
				"unknown order_type "+order.OrderType)
		}
	}
}
