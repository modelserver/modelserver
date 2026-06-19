package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	stripe "github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/client"
)

// StripeGatewayConfig holds configuration for the Stripe payment gateway.
type StripeGatewayConfig struct {
	SecretKey     string
	SuccessURL    string
	CancelURL     string
	DefaultLocale string
}

// StripeGateway implements the Gateway interface using Stripe Checkout Sessions.
type StripeGateway struct {
	sc  *client.API
	cfg StripeGatewayConfig
}

// NewStripeGateway creates a StripeGateway backed by the given config.
func NewStripeGateway(cfg StripeGatewayConfig) (*StripeGateway, error) {
	if cfg.SecretKey == "" {
		return nil, errors.New("stripe: secret_key is required")
	}
	sc := &client.API{}
	sc.Init(cfg.SecretKey, nil)
	return &StripeGateway{sc: sc, cfg: cfg}, nil
}

// Channel returns the payment channel identifier.
func (g *StripeGateway) Channel() string { return "stripe" }

// CreatePayment creates a Stripe Checkout Session and returns the session ID
// and hosted URL.
func (g *StripeGateway) CreatePayment(ctx context.Context, req *PaymentRequest) (*PaymentResult, error) {
	currency := strings.ToLower(req.Currency)
	if currency == "" {
		currency = "usd"
	}

	successURL := req.ReturnURL
	if successURL == "" {
		successURL = g.cfg.SuccessURL
	}
	cancelURL := g.cfg.CancelURL
	if cancelURL == "" {
		cancelURL = successURL
	}

	params := &stripe.CheckoutSessionParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL:        stripe.String(successURL),
		CancelURL:         stripe.String(cancelURL),
		ClientReferenceID: stripe.String(req.OutTradeNo),
		LineItems: []*stripe.CheckoutSessionLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:   stripe.String(currency),
				UnitAmount: stripe.Int64(req.Amount),
				ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
					Name: stripe.String(req.Description),
				},
			},
		}},
		PaymentIntentData: &stripe.CheckoutSessionPaymentIntentDataParams{},
	}

	// Session metadata — used in webhooks for tagging.
	params.AddMetadata("order_id", req.OutTradeNo)
	for k, v := range req.Metadata {
		params.AddMetadata(k, v)
	}

	// PaymentIntent metadata for downstream reconciliation.
	params.PaymentIntentData.AddMetadata("order_id", req.OutTradeNo)

	if req.CustomerEmail != "" {
		params.CustomerEmail = stripe.String(req.CustomerEmail)
	}
	if g.cfg.DefaultLocale != "" {
		params.Locale = stripe.String(g.cfg.DefaultLocale)
	}

	sess, err := g.sc.CheckoutSessions.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe checkout session: %w", err)
	}
	return &PaymentResult{
		TradeNo:    sess.ID,
		PaymentURL: sess.URL,
	}, nil
}
