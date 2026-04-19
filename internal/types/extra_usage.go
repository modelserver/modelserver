package types

import "time"

// Extra-usage transaction types. The ledger is append-only; every row is
// classified as exactly one of these.
const (
	ExtraUsageTxTopup     = "topup"
	ExtraUsageTxDeduction = "deduction"
	ExtraUsageTxRefund    = "refund"
	ExtraUsageTxAdjust    = "adjust"
)

// Extra-usage reason values recorded alongside each transaction for
// operations and audit. Guard/settlement paths only emit the first two;
// top-up/refund/adjust paths emit the latter three.
const (
	ExtraUsageReasonRateLimited       = "rate_limited"
	ExtraUsageReasonClientRestriction = "client_restriction"
	ExtraUsageReasonUserTopup         = "user_topup"
	ExtraUsageReasonAdminRefund       = "admin_refund"
	ExtraUsageReasonAdminAdjust       = "admin_adjust"
)

// ClientKind identifies the upstream client independent of trace-id source.
// `TraceSource` answers "where did the trace id come from"; `ClientKind`
// answers "which client is this, regardless of trace headers present".
// The subscription-eligibility decision (§3.2) depends on the latter.
const (
	ClientKindClaudeCode = "claude-code"
	ClientKindOpenCode   = "opencode"
	ClientKindOpenClaw   = "openclaw"
	ClientKindCodex      = "codex"
	ClientKindUnknown    = ""
)

// Publisher values for the global models table. These are the set the
// subscription-eligibility middleware decides on (§3.2); keep in sync with
// the migration backfill and admin validation.
const (
	PublisherAnthropic = "anthropic"
	PublisherOpenAI    = "openai"
	PublisherGoogle    = "google"
)

// Order-type values. Webhook delivery branches on this.
const (
	OrderTypeSubscription    = "subscription"
	OrderTypeExtraUsageTopup = "extra_usage_topup"
)

// ExtraUsageSettings is the per-project opt-in record. One row per project,
// keyed by project_id. Created on first top-up (with enabled=false) or via
// the dashboard; mutated by DeductExtraUsage / TopUpExtraUsage / admin PUT.
type ExtraUsageSettings struct {
	ProjectID       string    `json:"project_id"`
	Enabled         bool      `json:"enabled"`
	BalanceFen      int64     `json:"balance_fen"`
	MonthlyLimitFen int64     `json:"monthly_limit_fen"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ExtraUsageTransaction is one immutable ledger row.
type ExtraUsageTransaction struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	Type            string    `json:"type"`
	AmountFen       int64     `json:"amount_fen"`
	BalanceAfterFen int64     `json:"balance_after_fen"`
	RequestID       string    `json:"request_id,omitempty"`
	OrderID         string    `json:"order_id,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Description     string    `json:"description,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}
