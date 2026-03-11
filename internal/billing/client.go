package billing

import "context"

// PaymentRequest is sent to the payment provider to initiate a payment.
type PaymentRequest struct {
	OrderID     string            `json:"order_id"`
	ProductName string            `json:"product_name"`
	Channel     string            `json:"channel"`
	Currency    string            `json:"currency"`
	Amount      int64             `json:"amount"`
	NotifyURL   string            `json:"notify_url"`
	ReturnURL   string            `json:"return_url"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// PaymentResponse is returned by the payment provider after creating a payment.
type PaymentResponse struct {
	PaymentRef string `json:"payment_ref"`
	PaymentURL string `json:"payment_url"`
	Status     string `json:"status"`
}

// PaymentClient defines the interface for interacting with an external payment provider.
type PaymentClient interface {
	CreatePayment(ctx context.Context, req PaymentRequest) (*PaymentResponse, error)
}
