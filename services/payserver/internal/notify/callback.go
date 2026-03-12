package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type DeliveryPayload struct {
	OrderID    string `json:"order_id"`
	PaymentRef string `json:"payment_ref"`
	Status     string `json:"status"`
	PaidAmount int64  `json:"paid_amount"`
	PaidAt     string `json:"paid_at"`
}

type CallbackClient struct {
	url        string
	secret     string
	httpClient *http.Client
}

func NewCallbackClient(url, secret string, timeout time.Duration) *CallbackClient {
	return &CallbackClient{
		url:    url,
		secret: secret,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *CallbackClient) Send(ctx context.Context, payload DeliveryPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(c.secret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signature)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send callback: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("modelserver returned status %d", resp.StatusCode)
	}
	return nil
}

// uuidFromCompact restores a 32-hex-char compact UUID to the standard
// 8-4-4-4-12 format. If the input is already formatted or has an unexpected
// length it is returned unchanged.
func uuidFromCompact(s string) string {
	if len(s) != 32 {
		return s
	}
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
