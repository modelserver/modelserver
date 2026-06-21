package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/metrics"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

const (
	ctxExtraUsageIntent  contextKey = "extra_usage_intent"
	ctxExtraUsageContext contextKey = "extra_usage_context"
)

// ExtraUsageIntent marks a request that would otherwise have been blocked as
// a candidate for extra-usage fulfilment. Set by RateLimitMiddleware and
// consumed by ExtraUsageGuardMiddleware.
type ExtraUsageIntent struct {
	// Reason is "rate_limited" (credit window depleted) or
	// "client_restriction" (publisher/kind mismatch).
	Reason string
}

// ExtraUsageContext is written by the guard after all checks pass. The
// executor reads this to trigger post-request settlement.
type ExtraUsageContext struct {
	Reason            string
	BalanceFenAtEntry int64
	MonthlyLimitFen   int64
	MonthlySpentFen   int64
}

// withExtraUsageIntent tags the context with an intent reason. Safe to call
// with an empty reason — in that case no tag is attached.
func withExtraUsageIntent(ctx context.Context, reason string) context.Context {
	if reason == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxExtraUsageIntent, ExtraUsageIntent{Reason: reason})
}

// extraUsageIntentFromContext returns the intent and whether one is present.
func extraUsageIntentFromContext(ctx context.Context) (ExtraUsageIntent, bool) {
	i, ok := ctx.Value(ctxExtraUsageIntent).(ExtraUsageIntent)
	return i, ok
}

// withExtraUsageContext attaches the guard-approved context that the
// executor's settle hook reads to trigger billing.
func withExtraUsageContext(ctx context.Context, c ExtraUsageContext) context.Context {
	return context.WithValue(ctx, ctxExtraUsageContext, c)
}

// ExtraUsageContextFromContext returns the guard-approved settlement context
// and whether the request has been routed through extra usage.
func ExtraUsageContextFromContext(ctx context.Context) (ExtraUsageContext, bool) {
	c, ok := ctx.Value(ctxExtraUsageContext).(ExtraUsageContext)
	return c, ok
}

// extraUsageStore is the subset of *store.Store the guard needs. Extracted
// so tests can inject a fake without spinning up Postgres.
type extraUsageStore interface {
	GetExtraUsageSettings(projectID string) (*types.ExtraUsageSettings, error)
	GetMonthlyExtraSpendFen(projectID string, monthStart time.Time) (int64, error)
	// CreateRequest mirrors *store.Store.CreateRequest so the guard can
	// persist a row for 4xx rejections — without this, guard-level 429s are
	// invisible in the requests table (only a Prometheus counter is bumped),
	// which makes per-rejection investigation impossible.
	CreateRequest(r *types.Request) error
}

// ExtraUsageGuardMiddleware checks the global circuit breaker and per-project
// settings (enabled / balance / monthly limit) when an extra-usage intent was
// set upstream. It either approves (attaching ExtraUsageContext for the
// executor) or rejects with HTTP 429 + descriptive headers/body.
//
// When no intent is present the middleware is a no-op.
func ExtraUsageGuardMiddleware(cfg config.ExtraUsageConfig, st extraUsageStore, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// count_tokens is a zero-cost probe that the executor never
			// settles against extra_usage; gating it here would block
			// editor-side token counting for any anthropic request from a
			// non-claude-code client, which is hostile and serves no billing
			// purpose.
			if r.URL.Path == "/v1/messages/count_tokens" {
				next.ServeHTTP(w, r)
				return
			}

			intent, has := extraUsageIntentFromContext(r.Context())
			if !has {
				next.ServeHTTP(w, r)
				return
			}

			if !cfg.Enabled {
				msg := "extra usage temporarily disabled"
				writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
					Enabled: false,
					Message: msg,
				})
				emitGuardRejection(logger, st, r, intent.Reason, "global_disabled", msg)
				recordExtraUsageResult(intent.Reason, "rejected")
				return
			}

			project := ProjectFromContext(r.Context())
			if project == nil {
				// Auth should have populated this; fail safe.
				writeExtraUsageRejected(w, http.StatusInternalServerError, intent.Reason, guardStateRejected{
					Message: "missing project context",
				})
				return
			}

			settings, err := st.GetExtraUsageSettings(project.ID)
			if err != nil {
				logger.Error("extra_usage settings lookup failed", "error", err, "project_id", project.ID)
				writeExtraUsageRejected(w, http.StatusInternalServerError, intent.Reason, guardStateRejected{
					Message: "extra usage lookup failed",
				})
				return
			}

			bypass := settings != nil && settings.BypassBalanceCheck

			if !bypass {
				if settings == nil || !settings.Enabled {
					msg := rejectedMessage(intent.Reason, "not_enabled")
					writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
						Enabled: false,
						Message: msg,
					})
					emitGuardRejection(logger, st, r, intent.Reason, "not_enabled", msg)
					recordExtraUsageResult(intent.Reason, "rejected")
					return
				}
				if settings.BalanceFen <= 0 {
					msg := rejectedMessage(intent.Reason, "balance_depleted")
					writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
						Enabled:    true,
						BalanceFen: settings.BalanceFen,
						Message:    msg,
					})
					emitGuardRejection(logger, st, r, intent.Reason, "balance_depleted", msg)
					recordExtraUsageResult(intent.Reason, "rejected")
					return
				}
			}

			// Priceability pre-check: settle silently no-ops if we can't
			// compute a fen cost (catalog model missing, no DefaultCreditRate,
			// or creditPriceFen unset). Without this gate the request would
			// proceed to the upstream, return 200, get recorded as
			// is_extra_usage=true, and never debit the balance — a free ride.
			// Reject up front so the user sees the configuration gap.
			//
			// Skipped when bypass is on: bypass is an admin debugging flag
			// that explicitly opts out of billing enforcement, so refusing
			// requests for missing pricing data would defeat its purpose.
			if !bypass {
				if cfg.CreditPriceFen <= 0 {
					logger.Error("extra_usage_pricing_unavailable",
						"reason", "credit_price_unset", "project_id", project.ID)
					msg := rejectedMessage(intent.Reason, "model_unpriced")
					writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
						Enabled:    true,
						BalanceFen: settings.BalanceFen,
						Message:    msg,
					})
					emitGuardRejection(logger, st, r, intent.Reason, "model_unpriced_credit_price_unset", msg)
					recordExtraUsageResult(intent.Reason, "rejected")
					return
				}
				if m := ModelFromContext(r.Context()); m == nil || m.DefaultCreditRate == nil {
					modelName := ""
					if m != nil {
						modelName = m.Name
					}
					logger.Error("extra_usage_pricing_unavailable",
						"reason", "model_missing_default_rate",
						"project_id", project.ID, "model", modelName)
					msg := rejectedMessage(intent.Reason, "model_unpriced")
					writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
						Enabled:    true,
						BalanceFen: settings.BalanceFen,
						Message:    msg,
					})
					emitGuardRejection(logger, st, r, intent.Reason, "model_unpriced_missing_default_rate", msg)
					recordExtraUsageResult(intent.Reason, "rejected")
					return
				}
			}

			// Monthly-limit check: runs for both bypass and normal paths.
			// When bypass is on, settings != nil (bypass requires a row).
			var monthlySpent int64
			if settings.MonthlyLimitFen > 0 {
				spent, err := st.GetMonthlyExtraSpendFen(project.ID, store.MonthWindowStart())
				if err != nil {
					logger.Error("extra_usage monthly spend query failed", "error", err, "project_id", project.ID)
					writeExtraUsageRejected(w, http.StatusInternalServerError, intent.Reason, guardStateRejected{
						Message: "extra usage monthly check failed",
					})
					return
				}
				if spent >= settings.MonthlyLimitFen {
					msg := rejectedMessage(intent.Reason, "monthly_limit")
					writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
						Enabled:    true,
						BalanceFen: settings.BalanceFen,
						Message:    msg,
					})
					emitGuardRejection(logger, st, r, intent.Reason, "monthly_limit", msg)
					recordExtraUsageResult(intent.Reason, "rejected")
					return
				}
				monthlySpent = spent
			}

			ctx := withExtraUsageContext(r.Context(), ExtraUsageContext{
				Reason:            intent.Reason,
				BalanceFenAtEntry: settings.BalanceFen,
				MonthlyLimitFen:   settings.MonthlyLimitFen,
				MonthlySpentFen:   monthlySpent,
			})
			result := "allowed"
			if bypass {
				result = "allowed_bypass"
			}
			recordExtraUsageResult(intent.Reason, result)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type guardStateRejected struct {
	Enabled    bool
	BalanceFen int64
	Message    string
}

// writeExtraUsageRejected renders a 429 (typically) response with descriptive
// extra-usage headers and a JSON body. The envelope shape is the same as
// writeRateLimitError so client SDKs parsing 429 responses keep working.
func writeExtraUsageRejected(w http.ResponseWriter, status int, reason string, st guardStateRejected) {
	w.Header().Set("X-Extra-Usage-Required", "true")
	w.Header().Set("X-Extra-Usage-Reason", reason)
	w.Header().Set("X-Extra-Usage-Enabled", strconv.FormatBool(st.Enabled))
	w.Header().Set("X-Extra-Usage-Balance-Fen", strconv.FormatInt(st.BalanceFen, 10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    "rate_limit_error",
			"message": st.Message,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// rejectedMessage returns the user-facing message mapped from the (reason,
// sub-reason) pair. Keeps §5.4 phrasing in one place so future edits don't
// drift per-handler.
func rejectedMessage(reason, subReason string) string {
	switch reason {
	case "client_restriction":
		switch subReason {
		case "not_enabled":
			return "this client cannot use subscription for anthropic models; enable extra usage"
		case "balance_depleted":
			return "extra usage balance depleted for this client restriction"
		case "monthly_limit":
			return "extra usage monthly limit reached for this client restriction"
		}
	case "rate_limited":
		switch subReason {
		case "not_enabled":
			return "rate limit reached; enable extra usage to continue"
		case "balance_depleted":
			return "rate limit reached; extra usage balance depleted"
		case "monthly_limit":
			return "rate limit reached; extra usage monthly limit reached"
		case "model_unpriced":
			return "rate limit reached; extra usage cannot price this model (missing default rate or platform credit price)"
		}
	}
	if subReason == "model_unpriced" {
		return "extra usage cannot price this model (missing default rate or platform credit price)"
	}
	return fmt.Sprintf("extra usage unavailable: %s", subReason)
}

// recordExtraUsageResult bumps the Prometheus counter for guard decisions.
func recordExtraUsageResult(reason, result string) {
	metrics.IncExtraUsageRequest(reason, result)
}

// emitGuardRejection records a guard-level 429 in two places:
//
//   - slog at Warn level under the canonical message "extra_usage_rejected",
//     carrying every input that fed into the verdict (publisher, client_kind,
//     user-agent, client-kind detection signals, project/api-key/trace ids).
//     This is the primary triage surface — `grep extra_usage_rejected` plus
//     the JSON fields tells operators exactly why a request was rejected and
//     why the client wasn't recognised (e.g. missing metadata.user_id,
//     wrong User-Agent).
//   - requests table via st.CreateRequest, using the existing
//     RequestStatusRateLimited status so dashboards and queries that already
//     filter on `rate_limited` keep working. ExtraUsageReason distinguishes
//     the guard-side rejection from RateLimitMiddleware's classic 429s.
//
// Both sinks are best-effort: the DB row is skipped when project/api-key
// context is missing (5xx infra-failure paths), and the insert runs in a
// goroutine to keep the response path off the database. The slog line is
// always emitted because diagnosis matters even when attribution is partial.
func emitGuardRejection(logger *slog.Logger, st extraUsageStore, r *http.Request, reason, subReason, message string) {
	project := ProjectFromContext(r.Context())
	apiKey := APIKeyFromContext(r.Context())
	model := ModelFromContext(r.Context())
	traceID := TraceIDFromContext(r.Context())
	kind := ClientKindFromContext(r.Context())
	bodyModel := peekModel(r)

	publisher := ""
	modelName := bodyModel
	if model != nil {
		publisher = model.Publisher
		if model.Name != "" {
			modelName = model.Name
		}
	}
	projectID, apiKeyID, createdBy := "", "", ""
	if project != nil {
		projectID = project.ID
	}
	if apiKey != nil {
		apiKeyID = apiKey.ID
		createdBy = apiKey.CreatedBy
	}
	ua := r.Header.Get("User-Agent")
	opencodeHeader := strings.TrimSpace(r.Header.Get(openCodeTraceHeader)) != ""
	codexSession := codexSessionIDFromRequest(r) != ""
	openclawMatch := isOpenClawRequest(r)
	userIDShape := classifyClaudeUserIDShape(r)

	if logger != nil {
		logger.Warn("extra_usage_rejected",
			"reason", reason,
			"sub_reason", subReason,
			"message", message,
			"project_id", projectID,
			"api_key_id", apiKeyID,
			"created_by", createdBy,
			"trace_id", traceID,
			"model", modelName,
			"publisher", publisher,
			"client_kind", classifyClientKindLabel(kind),
			"user_agent", truncate(ua, 256),
			"client_ip", r.RemoteAddr,
			"path", r.URL.Path,
			"user_id_shape", userIDShape,
			"opencode_header", strconv.FormatBool(opencodeHeader),
			"codex_session", strconv.FormatBool(codexSession),
			"openclaw_match", strconv.FormatBool(openclawMatch),
		)
	}

	if st == nil {
		return
	}
	// buildRejectedRequestRow re-derives the same project/apiKey/model
	// the slog block above already extracted; we deliberately don't pass
	// them through to keep the helper context-driven (so callers from
	// other rejection sites — see ratelimit_middleware.go — share one
	// extraction path).
	req := buildRejectedRequestRow(r, types.RequestStatusRateLimited, message, reason)
	if req == nil {
		return
	}
	go st.CreateRequest(req)
}

// classifyClientKindLabel renders the empty unknown sentinel as a readable
// label for log scraping. Other kinds pass through unchanged.
func classifyClientKindLabel(kind string) string {
	if kind == types.ClientKindUnknown {
		return "unknown"
	}
	return kind
}

// classifyClaudeUserIDShape inspects the request body and returns a short
// label describing what (if anything) was found at metadata.user_id. This
// is the single most useful field for diagnosing why a request wasn't
// recognised as Claude Code:
//
//   - "absent"             — body has no metadata.user_id at all
//   - "json_session"       — JSON object containing a non-empty session_id
//   - "json_no_session"    — JSON object missing session_id
//   - "legacy_session"     — legacy user_<hex>_..._session_<uuid> string
//   - "legacy_no_session"  — non-JSON string that didn't match the legacy pattern
//
// Body access mirrors tryExtractClaudeCodeTraceID so the read doesn't
// consume the body for downstream handlers.
func classifyClaudeUserIDShape(r *http.Request) string {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		return "absent"
	}
	body, err := readAndRestoreBody(r)
	if err != nil || len(body) == 0 {
		return "absent"
	}
	res := gjson.GetBytes(body, "metadata.user_id")
	if !res.Exists() {
		return "absent"
	}
	raw := res.String()
	if raw == "" {
		return "absent"
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		if gjson.Get(raw, "session_id").String() != "" {
			return "json_session"
		}
		return "json_no_session"
	}
	if claudeUserIDLegacyPattern.MatchString(raw) {
		return "legacy_session"
	}
	return "legacy_no_session"
}

// truncate clips s to at most n bytes, suffixing with "…" when shortened.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
