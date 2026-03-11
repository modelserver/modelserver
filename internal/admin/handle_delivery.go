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

		// Fetch plan.
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

		// Deliver the order (transactional, uses FOR UPDATE on order row).
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
	}
}
