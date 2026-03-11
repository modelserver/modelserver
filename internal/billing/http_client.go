package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPPaymentClient implements PaymentClient using HTTP calls.
type HTTPPaymentClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewHTTPPaymentClient creates a new HTTP-based payment client.
func NewHTTPPaymentClient(baseURL, apiKey string) *HTTPPaymentClient {
	return &HTTPPaymentClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CreatePayment sends a payment creation request to the external payment provider.
func (c *HTTPPaymentClient) CreatePayment(ctx context.Context, req PaymentRequest) (*PaymentResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal payment request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/payments", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send payment request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("payment API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var payResp PaymentResponse
	if err := json.Unmarshal(respBody, &payResp); err != nil {
		return nil, fmt.Errorf("unmarshal payment response: %w", err)
	}
	return &payResp, nil
}
