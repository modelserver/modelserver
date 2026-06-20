package billing

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPPaymentClient_AuthHeaderJoinsTenantAndKey pins the bearer
// format payserver's tenantAuthMiddleware expects:
//
//	Authorization: Bearer <tenant_id>:<secret>
//
// The two halves come from config (BillingConfig.PaymentTenantID +
// BillingConfig.PaymentAPIKey). Joining at request time — rather than
// storing the joined string in config — keeps secret rotation to a
// single field. This test is the regression guard for that contract.
func TestHTTPPaymentClient_AuthHeaderJoinsTenantAndKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"payment_ref":"x","payment_url":"http://example/p","status":"pending"}`))
	}))
	defer srv.Close()

	client := NewHTTPPaymentClient(srv.URL, "0192abcd-1234-5678-9abc-def012345678", "supersecret")
	_, err := client.CreatePayment(t.Context(), PaymentRequest{
		OrderID: "ord-1", Channel: "stripe", Currency: "USD", Amount: 100,
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	want := "Bearer 0192abcd-1234-5678-9abc-def012345678:supersecret"
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

// TestHTTPPaymentClient_EmptyTenantIDStillSendsHeader documents the
// degraded behavior when an operator forgets to set PaymentTenantID:
// the header is sent as `Bearer :<secret>` and payserver responds 401
// with `malformed token`. This is the actionable failure mode (vs a
// silent success). Pin it so future refactors don't accidentally panic
// or silently elide the header.
func TestHTTPPaymentClient_EmptyTenantIDStillSendsHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := NewHTTPPaymentClient(srv.URL, "", "secret-only")
	_, _ = client.CreatePayment(t.Context(), PaymentRequest{OrderID: "x", Channel: "stripe"})
	want := "Bearer :secret-only"
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}
