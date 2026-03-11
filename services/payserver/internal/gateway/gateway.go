package gateway

import "context"

type Gateway interface {
	CreatePayment(ctx context.Context, req *PaymentRequest) (*PaymentResult, error)
	Channel() string
}

type PaymentRequest struct {
	OutTradeNo  string
	Description string
	Amount      int64
	NotifyURL   string
	ReturnURL   string
}

type PaymentResult struct {
	TradeNo    string
	PaymentURL string
}
