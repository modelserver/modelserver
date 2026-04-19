package types

import "time"

// Order status constants.
const (
	OrderStatusPending   = "pending"
	OrderStatusPaying    = "paying"
	OrderStatusPaid      = "paid"
	OrderStatusDelivered = "delivered"
	OrderStatusFailed    = "failed"
	OrderStatusCancelled = "cancelled"
)

// Order represents a purchase order for a plan or an extra-usage top-up.
// Subscription orders carry PlanID + Periods; top-up orders leave PlanID
// empty and carry ExtraUsageAmountFen.
type Order struct {
	ID                     string    `json:"id"`
	ProjectID              string    `json:"project_id"`
	PlanID                 string    `json:"plan_id,omitempty"`
	Periods                int       `json:"periods"`
	UnitPrice              int64     `json:"unit_price"`
	Amount                 int64     `json:"amount"`
	Currency               string    `json:"currency"`
	Status                 string    `json:"status"`
	Channel                string    `json:"channel,omitempty"`
	PaymentRef             string    `json:"payment_ref,omitempty"`
	PaymentURL             string    `json:"payment_url,omitempty"`
	ExistingSubscriptionID string    `json:"existing_subscription_id,omitempty"`
	Metadata               string    `json:"metadata,omitempty"`
	OrderType              string    `json:"order_type"`
	ExtraUsageAmountFen    int64     `json:"extra_usage_amount_fen,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}
