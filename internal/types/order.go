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

// Order represents a purchase order for a plan.
type Order struct {
	ID                     string    `json:"id"`
	ProjectID              string    `json:"project_id"`
	PlanID                 string    `json:"plan_id"`
	Periods                int       `json:"periods"`
	UnitPrice              int64     `json:"unit_price"`
	Amount                 int64     `json:"amount"`
	Currency               string    `json:"currency"`
	Status                 string    `json:"status"`
	PaymentRef             string    `json:"payment_ref,omitempty"`
	PaymentURL             string    `json:"payment_url,omitempty"`
	ExistingSubscriptionID string    `json:"existing_subscription_id,omitempty"`
	Metadata               string    `json:"metadata,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}
