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
	TraceSourceOpenClaw   = "openclaw"
	TraceSourceBody       = "body"
)

// Request records a single proxied API call.
type Request struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"project_id"`
	APIKeyID            string    `json:"api_key_id"`
	OAuthGrantID         string    `json:"oauth_grant_id,omitempty"`
	OAuthGrantClientName string    `json:"oauth_grant_client_name,omitempty"`
	TraceID             string    `json:"trace_id,omitempty"`
	MsgID               string    `json:"msg_id,omitempty"`
	Provider            string    `json:"provider,omitempty"`
	RequestKind         string    `json:"request_kind,omitempty"` // Wire-level endpoint kind; values from request_kind.go
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
	CreatedBy           string    `json:"created_by,omitempty"`
	CreatedByNickname   string    `json:"created_by_nickname,omitempty"`
	CreatedByPicture    string    `json:"created_by_picture,omitempty"`
	// Routing observability fields (populated by the new routing pipeline).
	UpstreamID  string  `json:"upstream_id,omitempty"`
	RouteID     string  `json:"route_id,omitempty"`
	GroupID     string  `json:"group_id,omitempty"`
	Attempt     int     `json:"attempt,omitempty"`
	RetryReason string  `json:"retry_reason,omitempty"`
	SelectionMs float64            `json:"selection_ms,omitempty"`
	Metadata    map[string]string  `json:"metadata,omitempty"`
	// Extra-usage attribution. Set by Executor.settleExtraUsage when the
	// request was routed through the extra-usage path; zeroed otherwise.
	IsExtraUsage      bool   `json:"is_extra_usage,omitempty"`
	ExtraUsageCostFen int64  `json:"extra_usage_cost_fen,omitempty"`
	ExtraUsageReason  string `json:"extra_usage_reason,omitempty"`
	HttpLogPath       string `json:"http_log_path,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
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
