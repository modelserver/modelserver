package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

const testStripeSecret = "whsec_test_dummy_secret_value_long_enough"

// signStripePayload computes the same v1 HMAC Stripe sends so tests can
// drive ServeHTTP without monkey-patching the SDK.
func signStripePayload(t *testing.T, body []byte, secret string) (header string, ts int64) {
	t.Helper()
	ts = time.Now().Unix()
	signedPayload := fmt.Sprintf("%d.%s", ts, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	sig := hex.EncodeToString(mac.Sum(nil))
	header = fmt.Sprintf("t=%d,v1=%s", ts, sig)
	return
}

func buildCheckoutSessionEvent(orderID string, amount int64, paymentStatus string) []byte {
	ev := map[string]any{
		"id":          "evt_test_1",
		"object":      "event",
		"type":        "checkout.session.completed",
		"api_version": "2026-05-27.dahlia",
		"created":     time.Now().Unix(),
		"data": map[string]any{
			"object": map[string]any{
				"id":                  "cs_test_xyz",
				"object":              "checkout.session",
				"client_reference_id": orderID,
				"amount_total":        amount,
				"currency":            "usd",
				"payment_status":      paymentStatus,
			},
		},
	}
	b, _ := json.Marshal(ev)
	return b
}

func TestStripeNotify_BadSignature(t *testing.T) {
	h, _, _ := newStripeHarness(t)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader([]byte("{}")))
	req.Header.Set("Stripe-Signature", "t=0,v1=deadbeef")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad sig: got %d, want 400", w.Code)
	}
}

func TestStripeNotify_HappyPath(t *testing.T) {
	h, st, cb := newStripeHarness(t)
	orderID := seedPendingPayment(t, st, "stripe", 2000)

	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("happy path: got %d, want 200", w.Code)
	}
	p, _ := st.GetPaymentByOrderID(orderID)
	if p.Status != "paid" {
		t.Errorf("status = %q, want paid", p.Status)
	}
	if cb.calls() != 1 {
		t.Errorf("callback calls = %d, want 1", cb.calls())
	}
}

func TestStripeNotify_NonCheckoutCompletedAcked(t *testing.T) {
	h, _, _ := newStripeHarness(t)
	body := []byte(`{"id":"evt_test_2","object":"event","type":"payment_intent.created","api_version":"2026-05-27.dahlia","data":{"object":{}},"created":` +
		strconv.FormatInt(time.Now().Unix(), 10) + `}`)
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ignored event: got %d, want 200", w.Code)
	}
}

func TestStripeNotify_PaymentStatusNotPaidAcked(t *testing.T) {
	h, st, cb := newStripeHarness(t)
	orderID := seedPendingPayment(t, st, "stripe", 2000)
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "unpaid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if cb.calls() != 0 {
		t.Fatalf("callback fired on unpaid event: %d calls", cb.calls())
	}
}

func TestStripeNotify_PaymentNotFound(t *testing.T) {
	h, _, _ := newStripeHarness(t)
	body := buildCheckoutSessionEvent("00000000000000000000000000000000", 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing payment: got %d, want 404", w.Code)
	}
}

func TestStripeNotify_ChannelMismatch(t *testing.T) {
	h, st, _ := newStripeHarness(t)
	orderID := seedPendingPayment(t, st, "wechat", 2000) // wrong channel
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("channel mismatch: got %d, want 400", w.Code)
	}
}

func TestStripeNotify_AmountMismatch(t *testing.T) {
	h, st, _ := newStripeHarness(t)
	orderID := seedPendingPayment(t, st, "stripe", 2000)
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 999, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("amount mismatch: got %d, want 400", w.Code)
	}
}

func TestStripeNotify_DuplicateAlreadyPaidAndCallbackSuccess(t *testing.T) {
	h, st, cb := newStripeHarness(t)
	orderID := seedPaidPayment(t, st, "stripe", 2000) // status=paid, callback_status=success
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if cb.calls() != 0 {
		t.Fatalf("duplicate triggered callback: %d", cb.calls())
	}
}

func TestStripeNotify_CallbackFailureIncrementsRetries(t *testing.T) {
	h, st, cb := newStripeHarness(t)
	cb.setFail(true)
	orderID := seedPendingPayment(t, st, "stripe", 2000)
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	p, _ := st.GetPaymentByOrderID(orderID)
	if p.CallbackRetries != 1 {
		t.Errorf("retries = %d, want 1", p.CallbackRetries)
	}
	if p.CallbackStatus == "success" {
		t.Errorf("callback marked success despite failure")
	}
}

// --- harness ---

// stubCallback tracks calls to the fake modelserver endpoint.
type stubCallback struct {
	callCount atomic.Int64
	shouldFail atomic.Bool
}

func (s *stubCallback) calls() int {
	return int(s.callCount.Load())
}

func (s *stubCallback) setFail(v bool) {
	s.shouldFail.Store(v)
}

func newStripeHarness(t *testing.T) (*StripeNotifyHandler, *store.Store, *stubCallback) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := openTestPayserverStore(t)
	cb := &stubCallback{}
	cbClient := newStubCallbackClient(t, cb)
	return NewStripeNotifyHandler(testStripeSecret, st, cbClient, logger), st, cb
}

// openTestPayserverStore opens a *store.Store against the test DB.
// It skips the test if PAYSERVER_TEST_DB_URL is not set, and truncates the
// payments table so each test starts with a clean slate.
func openTestPayserverStore(t *testing.T) *store.Store {
	t.Helper()
	dbURL := os.Getenv("PAYSERVER_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("PAYSERVER_TEST_DB_URL not set; skipping DB-dependent test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.New(dbURL, logger)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Truncate for a clean slate before each test.
	if _, err := st.Pool().Exec(context.Background(), "TRUNCATE payments"); err != nil {
		t.Fatalf("truncate payments: %v", err)
	}
	return st
}

// newStubCallbackClient creates a *CallbackClient backed by a temporary
// httptest.Server that records calls and optionally returns 500.
func newStubCallbackClient(t *testing.T, cb *stubCallback) *CallbackClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cb.shouldFail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		cb.callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return NewCallbackClient(srv.URL, "test-secret", 5*time.Second)
}

// seedPendingPayment inserts a payment with status=pending and returns its order ID.
func seedPendingPayment(t *testing.T, st *store.Store, channel string, amount int64) string {
	t.Helper()
	orderID := newTestUUID(t)
	p := &store.Payment{
		OrderID: orderID,
		Channel: channel,
		Amount:  amount,
		Status:  "pending",
	}
	_, err := st.InsertOrGetPayment(p)
	if err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	return orderID
}

// seedPaidPayment inserts a payment with status=paid and callback_status=success.
func seedPaidPayment(t *testing.T, st *store.Store, channel string, amount int64) string {
	t.Helper()
	orderID := seedPendingPayment(t, st, channel, amount)
	if _, err := st.MarkPaymentPaid(orderID, "cs_seed", `{}`, time.Now()); err != nil {
		t.Fatalf("seed paid: %v", err)
	}
	if err := st.MarkCallbackSuccess(orderID); err != nil {
		t.Fatalf("seed callback success: %v", err)
	}
	return orderID
}

// newTestUUID generates a random UUID (version 4) without requiring an external package.
func newTestUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand uuid: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
