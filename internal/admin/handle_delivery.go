package admin

import (
	"net/http"

	"github.com/modelserver/modelserver/internal/billing"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleDeliveryWebhook(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload billing.DeliveryPayload
		if err := decodeBody(r, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if payload.OrderID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "order_id is required")
			return
		}

		// Fetch and validate order.
		order, err := st.GetOrderByID(payload.OrderID)
		if err != nil || order == nil {
			writeError(w, http.StatusNotFound, "not_found", "order not found")
			return
		}

		// Idempotency: if already delivered, return success without re-processing.
		if order.Status == types.OrderStatusDelivered {
			writeData(w, http.StatusOK, map[string]interface{}{
				"order_id": order.ID,
				"status":   "delivered",
			})
			return
		}

		if order.Status != types.OrderStatusPaying {
			writeError(w, http.StatusConflict, "invalid_status", "order status is "+order.Status+", expected paying")
			return
		}
		if payload.PaidAmount < order.Amount {
			writeError(w, http.StatusBadRequest, "insufficient_payment", "paid amount is less than order amount")
			return
		}

		// Branch by order_type. Subscription orders carry a plan_id and go
		// through the existing delivery path; extra-usage top-ups apply
		// directly to the project's balance via an idempotent ledger write.
		switch order.OrderType {
		case types.OrderTypeExtraUsageTopup:
			bal, err := deliverExtraUsageTopupOrder(st, order)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "delivery_failed", "failed to deliver topup: "+err.Error())
				return
			}
			writeData(w, http.StatusOK, map[string]interface{}{
				"order_id":    order.ID,
				"status":      "delivered",
				"balance_fen": bal,
			})
			return

		case "", types.OrderTypeSubscription:
			// plan_id is nullable since migration 017, but subscription orders
			// must carry one. Guard so we return a clean error instead of
			// calling GetPlanByID("") and surfacing a generic 500.
			if order.PlanID == "" {
				writeError(w, http.StatusInternalServerError, "internal",
					"subscription order has no plan_id")
				return
			}
			plan, err := st.GetPlanByID(order.PlanID)
			if err != nil || plan == nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to fetch plan")
				return
			}

			// Fetch project.
			project, err := st.GetProjectByID(order.ProjectID)
			if err != nil || project == nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to fetch project")
				return
			}

			sub, err := st.DeliverOrder(order.ID, plan, project)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "delivery_failed", "failed to deliver order")
				return
			}

			writeData(w, http.StatusOK, map[string]interface{}{
				"order_id":        order.ID,
				"subscription_id": sub.ID,
				"status":          "delivered",
			})
			return

		default:
			writeError(w, http.StatusBadRequest, "bad_request", "unknown order_type "+order.OrderType)
			return
		}
	}
}
