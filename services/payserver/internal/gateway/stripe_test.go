package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	stripe "github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/client"
	"github.com/stripe/stripe-go/v86/form"
)

// fakeStripeBackend captures the outgoing form-encoded body so the test
// can assert which parameters are sent to Stripe without doing any real
// network calls.
type fakeStripeBackend struct {
	captured url.Values
	respond  func(method, path string) (string, error)
}

func (b *fakeStripeBackend) Call(method, path, key string, params stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	// Encode params the same way the real backend would for v1 endpoints.
	formValues := &form.Values{}
	if params != nil {
		form.AppendTo(formValues, params)
	}
	b.captured = formValues.ToValues()

	// Return canned JSON response.
	jsonBody, err := b.respond(method, path)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(jsonBody), v)
}

func (b *fakeStripeBackend) CallStreaming(method, path, key string, params stripe.ParamsContainer, v stripe.StreamingLastResponseSetter) error {
	return errors.New("unused")
}

func (b *fakeStripeBackend) CallRaw(method, path, key string, body []byte, params *stripe.Params, v stripe.LastResponseSetter) error {
	return errors.New("unused")
}

func (b *fakeStripeBackend) CallMultipart(method, path, key, boundary string, body *bytes.Buffer, params *stripe.Params, v stripe.LastResponseSetter) error {
	return errors.New("unused")
}

func (b *fakeStripeBackend) SetMaxNetworkRetries(maxNetworkRetries int64) {}

func newFakeClient(b *fakeStripeBackend) *client.API {
	sc := &client.API{}
	sc.Init("sk_test_dummy", &stripe.Backends{
		API:         b,
		Connect:     b,
		Uploads:     b,
		MeterEvents: b,
	})
	return sc
}

func TestStripeCreatePayment_ParamsAssembly(t *testing.T) {
	be := &fakeStripeBackend{
		respond: func(method, path string) (string, error) {
			if strings.HasSuffix(path, "/v1/checkout/sessions") {
				return `{"id":"cs_test_abc123","url":"https://checkout.stripe.com/c/cs_test_abc123"}`, nil
			}
			return `{}`, nil
		},
	}

	g := &StripeGateway{
		sc: newFakeClient(be),
		cfg: StripeGatewayConfig{
			SecretKey:     "sk_test_dummy",
			SuccessURL:    "https://config.example/success",
			CancelURL:     "https://config.example/cancel",
			DefaultLocale: "en",
		},
	}

	res, err := g.CreatePayment(context.Background(), &PaymentRequest{
		OutTradeNo:    "order123",
		Description:   "Pro Plan",
		Amount:        2000,
		Currency:      "USD",
		ReturnURL:     "https://from-request.example/back",
		CustomerEmail: "user@example.com",
		Metadata: map[string]string{
			"plan_slug": "pro",
			"periods":   "1",
		},
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	// PaymentResult shape.
	if res.TradeNo != "cs_test_abc123" {
		t.Errorf("TradeNo = %q, want cs_test_abc123", res.TradeNo)
	}
	if !strings.HasPrefix(res.PaymentURL, "https://checkout.stripe.com/") {
		t.Errorf("PaymentURL = %q", res.PaymentURL)
	}

	// Outgoing params: success_url prefers request, currency lower-cased,
	// amount in cents, ClientReferenceID, metadata, email, locale.
	got := be.captured
	if got.Get("success_url") != "https://from-request.example/back" {
		t.Errorf("success_url = %q, want request-provided value", got.Get("success_url"))
	}
	if got.Get("cancel_url") != "https://config.example/cancel" {
		t.Errorf("cancel_url = %q, want config fallback", got.Get("cancel_url"))
	}
	if got.Get("client_reference_id") != "order123" {
		t.Errorf("client_reference_id = %q", got.Get("client_reference_id"))
	}
	if got.Get("customer_email") != "user@example.com" {
		t.Errorf("customer_email = %q", got.Get("customer_email"))
	}
	if got.Get("locale") != "en" {
		t.Errorf("locale = %q", got.Get("locale"))
	}
	if got.Get("line_items[0][price_data][currency]") != "usd" {
		t.Errorf("currency = %q (expect lowercase usd)", got.Get("line_items[0][price_data][currency]"))
	}
	if got.Get("line_items[0][price_data][unit_amount]") != "2000" {
		t.Errorf("unit_amount = %q", got.Get("line_items[0][price_data][unit_amount]"))
	}
	if got.Get("metadata[order_id]") != "order123" {
		t.Errorf("metadata.order_id missing, got %q", got.Get("metadata[order_id]"))
	}
	if got.Get("metadata[plan_slug]") != "pro" {
		t.Errorf("metadata.plan_slug = %q", got.Get("metadata[plan_slug]"))
	}
}

func TestStripeCreatePayment_DefaultsWhenRequestEmpty(t *testing.T) {
	be := &fakeStripeBackend{
		respond: func(method, path string) (string, error) {
			if strings.HasSuffix(path, "/v1/checkout/sessions") {
				return `{"id":"cs_test_def456","url":"https://checkout.stripe.com/c/cs_test_def456"}`, nil
			}
			return `{}`, nil
		},
	}

	g := &StripeGateway{
		sc: newFakeClient(be),
		cfg: StripeGatewayConfig{
			SecretKey:  "sk_test_dummy",
			SuccessURL: "https://config.example/success",
			CancelURL:  "https://config.example/cancel",
			// No DefaultLocale set.
		},
	}

	// Send a request with no Currency / ReturnURL / CustomerEmail / Metadata.
	res, err := g.CreatePayment(context.Background(), &PaymentRequest{
		OutTradeNo:  "order456",
		Description: "Basic Plan",
		Amount:      500,
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	if res.TradeNo != "cs_test_def456" {
		t.Errorf("TradeNo = %q, want cs_test_def456", res.TradeNo)
	}

	got := be.captured

	// Currency defaults to "usd" when not specified.
	if got.Get("line_items[0][price_data][currency]") != "usd" {
		t.Errorf("currency = %q, want default usd", got.Get("line_items[0][price_data][currency]"))
	}

	// success_url falls back to config when ReturnURL is empty.
	if got.Get("success_url") != "https://config.example/success" {
		t.Errorf("success_url = %q, want config fallback", got.Get("success_url"))
	}

	// customer_email param must NOT be set when req.CustomerEmail is empty.
	if got.Get("customer_email") != "" {
		t.Errorf("customer_email should not be set, got %q", got.Get("customer_email"))
	}

	// locale should NOT be set when config has no DefaultLocale.
	if got.Get("locale") != "" {
		t.Errorf("locale should not be set when DefaultLocale is empty, got %q", got.Get("locale"))
	}

	// cancel_url falls back to config.
	if got.Get("cancel_url") != "https://config.example/cancel" {
		t.Errorf("cancel_url = %q, want config fallback", got.Get("cancel_url"))
	}
}
