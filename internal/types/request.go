package types

import "time"

// Request status constants.
const (
	RequestStatusProcessing  = "processing"
	RequestStatusSuccess     = "success"
	RequestStatusError       = "error"
	RequestStatusRateLimited = "rate_limited"
)

// TraceSource constants identify how a trace ID was associated with a request.
const (
	TraceSourceHeader     = "header"
	TraceSourceAuto       = "auto"
	TraceSourceClaudeCode = "claude-code"
	TraceSourceOpenCode   = "opencode"
	TraceSourceCodex      = "codex"
	TraceSourceBody       = "body"
)

// Request records a single proxied API call.
type Request struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"project_id"`
	APIKeyID            string    `json:"api_key_id"`
	ChannelID           string    `json:"channel_id"`
	TraceID             string    `json:"trace_id,omitempty"`
	MsgID               string    `json:"msg_id,omitempty"`
	Provider            string    `json:"provider"`
	Model               string    `json:"model"`
	Streaming           bool      `json:"streaming"`
	Status              string    `json:"status"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CreditsConsumed     float64   `json:"-"`
	LatencyMs           int64     `json:"latency_ms"`
	TTFTMs              int64     `json:"ttft_ms"`
	ClientIP            string    `json:"client_ip,omitempty"`
	ErrorMessage        string    `json:"error_message,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// Trace groups related requests under a shared trace identifier.
type Trace struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TokenUsage holds raw token counts collected from a single proxied response.
type TokenUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}
