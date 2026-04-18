# Extra Usage 设计

> Status: Draft (supersedes `docs/design-extra-usage.md` from 2026-03-21)
> Author: 对话协作产出
> Date: 2026-04-18

## 一、背景与目标

modelserver 目前只有 subscription 模型：项目绑定 plan，plan 内含 credit 窗口额度（例如 5h 50000 credits）与 classic 限流（RPM/TPM/RPD/TPD）。超限后服务器返回 429，请求无法继续。

参考 [Claude 官方 Extra Usage 设计](https://support.claude.com/en/articles/12429409-manage-extra-usage-for-paid-claude-plans)，我们引入**按量预付费**机制作为 subscription 的补充。与官方不同的是，modelserver 还需要处理一种"客户端限制"场景。

### 触发 extra usage 的两种场景

**(a) Credit 规则命中**：用户的订阅 credit 窗口（例如 5h/50000 credits）已消费殆尽。

**(b) 客户端不符合 coding plan 要求**：当前规则——对 `publisher = 'anthropic'` 的模型，只有来自 Claude Code CLI（`ClientKind = 'claude-code'`）的请求才能消费 subscription；其他客户端（OpenCode、OpenClaw、Codex 等）必须走 extra usage。其他 publisher 的模型暂无额外限制。

> **关于 `ClientKind` vs `TraceSource`**：现有 `TraceMiddleware` 产出 `TraceSource`，但其检测顺序优先尊重 `X-Trace-Id` 等 header，会把带 trace header 的 Claude Code 请求误标为 `"header"`。我们因此**并行**派生一个 `ClientKind` 字段，只做客户端归属检测（与 header/body 的 trace 抽取解耦）。详见 §3.2。

**不触发**：classic 规则（RPM/TPM/RPD/TPD）命中——这些仍是**硬限**，用于保护上游与防滥用，与余额无关。

### 计费原则

Extra usage 按**官方 API 价格**计费——具体实现为使用 catalog 的 `default_credit_rate`（= 官方 API 价 / 7.5 USD），忽略 plan 层面的任何折扣覆盖。余额以 CNY fen 存储，通过配置 `credit_price_fen` 完成 credits → fen 转换（默认 5438，即 ¥54.38 / 1M credits）。

### 启用模型

项目级 opt-in：默认 `enabled = false`，用户须在 dashboard 主动开启并完成首次充值。

---

## 二、数据模型

### 2.1 新表：`extra_usage_settings`（每项目一行）

```sql
CREATE TABLE extra_usage_settings (
    project_id        UUID        PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled           BOOLEAN     NOT NULL DEFAULT FALSE,
    balance_fen       BIGINT      NOT NULL DEFAULT 0 CHECK (balance_fen >= 0),
    monthly_limit_fen BIGINT      NOT NULL DEFAULT 0,   -- 0 = 不限
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### 2.2 新表：`extra_usage_transactions`（不可变 ledger）

```sql
CREATE TABLE extra_usage_transactions (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id        UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type              TEXT        NOT NULL
                      CHECK (type IN ('topup','deduction','refund','adjust')),
    amount_fen        BIGINT      NOT NULL,            -- 正=入账；负=出账
    balance_after_fen BIGINT      NOT NULL CHECK (balance_after_fen >= 0),
    request_id        UUID        NULL REFERENCES requests(id) ON DELETE SET NULL,
    order_id          UUID        NULL REFERENCES orders(id)   ON DELETE SET NULL,
    reason            TEXT        NOT NULL DEFAULT '',
    description       TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX ON extra_usage_transactions (project_id, created_at DESC);
CREATE INDEX ON extra_usage_transactions (project_id, type, created_at)
    WHERE type = 'deduction';
-- 幂等：同一 topup order 写入 ledger 只允许一次
CREATE UNIQUE INDEX ON extra_usage_transactions (order_id)
    WHERE type = 'topup' AND order_id IS NOT NULL;
```

`reason` 值集合：`rate_limited` | `client_restriction` | `user_topup` | `admin_refund` | `admin_adjust` | `''`。`adjust` 类型保留给未来管理员手动调账。

### 2.3 `requests` 表扩列

```sql
ALTER TABLE requests
    ADD COLUMN is_extra_usage       BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN extra_usage_cost_fen BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN extra_usage_reason   TEXT    NOT NULL DEFAULT '';
```

### 2.4 `models` 表扩列：`publisher`

```sql
ALTER TABLE models ADD COLUMN publisher TEXT NOT NULL DEFAULT '';
UPDATE models SET publisher='anthropic' WHERE name LIKE 'claude-%';
UPDATE models SET publisher='openai'    WHERE name ~ '^(gpt-|o[0-9]|chatgpt-|text-)';
UPDATE models SET publisher='google'    WHERE name LIKE 'gemini-%';
```

管理员 UI 创建/编辑模型时**必须**填 `publisher`（非空字符串）：`internal/admin/models.go` 的 `UpsertModel` 处校验，空值返 400。需求 (b) 的"Claude 系列"判定：`publisher = 'anthropic'`。将来如要给其他 publisher 做限制（例如"对 openai 模型也限制某些客户端"），只在 `SubscriptionEligibilityMW`（见 3.2）改判定逻辑即可。

**`publisher` vs 既有 `Metadata.ProviderHint`**：`provider_hint` 是 UI 展示用的软提示（可为空、可自由拼写，用于图标/分类）；`publisher` 是**业务决策**字段（必填、受控取值枚举 `anthropic/openai/google/...`）。两者独立存在、互不替代。

**Backfill 后仍为空的兜底**：若运行时遇到 `publisher == ''` 的 model（理论上 migration backfill + admin 校验已封堵），`SubscriptionEligibilityMW` 记 warning 并**按 "allow subscription" 处理**（不影响既有流量），同时 Prometheus `extra_usage_missing_publisher_total` +1。运维看到该指标非 0 即需补数据。

### 2.5 `orders` 表扩展，支持 extra-usage 充值单

```sql
ALTER TABLE orders
    ADD COLUMN order_type TEXT NOT NULL DEFAULT 'subscription'
        CHECK (order_type IN ('subscription','extra_usage_topup')),
    ADD COLUMN extra_usage_amount_fen BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders ALTER COLUMN plan_id DROP NOT NULL;
```

Topup 订单约定：`plan_id IS NULL`、`order_type='extra_usage_topup'`、`periods=1`（沿用现有 NOT NULL 默认值，无需改 schema）、`amount = extra_usage_amount_fen = unit_price`。

### 2.6 迁移文件

全部写入 `internal/store/migrations/017_extra_usage.sql`，单个迁移原子化。所有现有行 backfill 到默认值（新列均 `NOT NULL DEFAULT`，无破坏性变更）。

---

## 三、请求流程与决策逻辑

### 3.1 中间件链路

```
当前:  Handler → AuthMW → TraceMW → RateLimitMW → Executor
改造:  Handler → AuthMW → TraceMW → ResolveModelMW
         → SubscriptionEligibilityMW → RateLimitMW → ExtraUsageGuardMW → Executor
```

`TraceMW` 同时改造：在现有 `TraceSource` 逻辑之外，**独立派生 `ClientKind`** 字段写入 context（详见 3.2）。

新增三个中间件（或从 handler 抽出）：

- `ResolveModelMW`：从 handler 前置 30 行抽出——把 raw model 名解析为 canonical `*types.Model`，写入 `RequestContext.Model` 字段（或 context value）供下游使用（特别是 executor 的扣费钩子需要读 `DefaultCreditRate`）。
- `SubscriptionEligibilityMW`：判定需求 (b)，只写 context 标记，不拦截。
- `ExtraUsageGuardMW`：在 intent 存在时做三项校验（enabled / balance / monthly），通过则标记放行，失败则拒绝。

### 3.2 `ClientKind` 派生（TraceMW 改造）与 `SubscriptionEligibilityMW`

**为什么引入 `ClientKind`**：`TraceSource` 的检测顺序优先 `X-Trace-Id` / 自定义 header，带 header 的 Claude Code 请求会被标为 `"header"` 而非 `"claude-code"`，使需求 (b) 误判。`ClientKind` 只做客户端归属判定，与 trace id 来源解耦。

在 `TraceMiddleware` 末尾追加一段 —— **无视** header 检测结果，独立判断：

```go
// trace_middleware.go：在原 deriveTraceID 之后
kind := deriveClientKind(r)
ctx = context.WithValue(ctx, ctxClientKind, kind)

// deriveClientKind 复用已有的 tryExtractClaudeCodeTraceID / isOpenClawRequest，
// 但不短路于 trace source，只做归属判定。
func deriveClientKind(r *http.Request) string {
    if id, _ := tryExtractClaudeCodeTraceID(r); id != "" {
        return types.ClientKindClaudeCode   // "claude-code"
    }
    ua := strings.ToLower(r.Header.Get("User-Agent"))
    switch {
    case strings.Contains(ua, "opencode/"),
         strings.TrimSpace(r.Header.Get("X-Opencode-Session")) != "":
        return types.ClientKindOpenCode
    case isOpenClawRequest(r):
        return types.ClientKindOpenClaw
    case strings.TrimSpace(r.Header.Get("Session_id")) != "":
        return types.ClientKindCodex
    }
    return types.ClientKindUnknown   // ""
}
```

常量集中在 `internal/types/request.go`：`ClientKindClaudeCode = "claude-code"` 等。

`SubscriptionEligibilityMW`：

```go
func SubscriptionEligibilityMW(next http.Handler) http.Handler {
    return func(w, r) {
        m    := ModelFromContext(r.Context())
        kind := ClientKindFromContext(r.Context())

        eligible := true
        reason   := ""
        switch {
        case m.Publisher == "":
            // 数据空洞：记指标但放行 subscription（§2.4 兜底）
            metrics.ExtraUsageMissingPublisher.Inc()
        case m.Publisher == "anthropic" && kind != types.ClientKindClaudeCode:
            eligible = false
            reason   = "client_restriction"
        }

        ctx := context.WithValue(r.Context(), ctxSubscriptionEligibility,
            SubscriptionEligibility{Eligible: eligible, Reason: reason})
        next.ServeHTTP(w, r.WithContext(ctx))
    }
}
```

### 3.3 `CompositeRateLimiter` 接口变更

当前（`internal/ratelimit/composite.go:32`）：

```go
PreCheck(ctx, projectID, apiKeyID, model, policy) (bool, time.Duration, error)
```

两处改动：`PreCheck` 返回 `PreCheckResult` 结构体；新增同签名的 `PreCheckClassicOnly`（跳过 credit 规则，只跑 classic）：

```go
type PreCheckResult struct {
    Allowed    bool
    RetryAfter time.Duration
    LimitType  string // "credit" | "classic" | ""
    HitRuleID  string // 可选，便于审计日志
}

PreCheck(ctx, projectID, apiKeyID, model, policy) (PreCheckResult, error)
PreCheckClassicOnly(ctx, projectID, apiKeyID, model, policy) (PreCheckResult, error)
```

`PreCheckClassicOnly` 语义：`Allowed=true` 表示 classic 通过（返回 `LimitType=""`）；`Allowed=false` 必定 `LimitType="classic"`。给需求 (b) bypass 路径使用。

现有所有 `PreCheck` 调用点（`internal/proxy/ratelimit_middleware.go` 等）要同步更新到读 `res.Allowed / res.RetryAfter`。

### 3.4 `RateLimitMW` 改造

```go
func RateLimitMW(next) {
    elig := SubscriptionEligibilityFromContext(ctx)

    if !elig.Eligible {
        // 需求 b：仍要跑 classic 保护上游
        res, _ := limiter.PreCheckClassicOnly(ctx, ...)
        if !res.Allowed {
            writeRateLimitError(w, res.RetryAfter)
            return
        }
        ctx = withExtraUsageIntent(ctx, elig.Reason) // "client_restriction"
        next.ServeHTTP(w, r.WithContext(ctx))
        return
    }

    res, _ := limiter.PreCheck(ctx, ...)
    if res.Allowed {
        next.ServeHTTP(w, r)            // 正常走 subscription
        return
    }
    if res.LimitType == "credit" {
        ctx = withExtraUsageIntent(ctx, "rate_limited")
        next.ServeHTTP(w, r.WithContext(ctx))
        return
    }
    writeRateLimitError(w, res.RetryAfter) // classic 或其他 → 硬拒
}
```

### 3.5 `ExtraUsageGuardMW`

```go
func ExtraUsageGuardMW(next) {
    intent, has := extraUsageIntentFromContext(ctx)
    if !has {
        next.ServeHTTP(w, r)
        return
    }

    // 全局熔断
    if !cfg.ExtraUsage.Enabled {
        writeExtraUsageRejected(w, "extra usage temporarily disabled", intent.Reason)
        return
    }

    settings, err := store.GetExtraUsageSettings(projectID)
    if err != nil { /* 500 */ }
    if settings == nil || !settings.Enabled {
        writeExtraUsageRejected(w, "extra usage not enabled", intent.Reason)
        return
    }
    if settings.BalanceFen <= 0 {
        writeExtraUsageRejected(w, "extra usage balance depleted", intent.Reason)
        return
    }
    if settings.MonthlyLimitFen > 0 {
        spent, _ := store.GetMonthlyExtraSpendFen(projectID)
        if spent >= settings.MonthlyLimitFen {
            writeExtraUsageRejected(w, "extra usage monthly limit reached", intent.Reason)
            return
        }
    }

    ctx = withExtraUsageContext(ctx, ExtraUsageContext{
        Reason:            intent.Reason,
        BalanceFenAtEntry: settings.BalanceFen,
    })
    next.ServeHTTP(w, r.WithContext(ctx))
}
```

### 3.6 决策真值表

| Publisher | ClientKind=claude-code | Credit 命中 | Classic 命中 | 结果 |
|-----------|:----:|:----:|:----:|------|
| anthropic | 是 | 否 | 否 | ✅ subscription |
| anthropic | 是 | ✅ | 否 | ⚡ extra usage (`rate_limited`) |
| anthropic | 是 | 否 | ✅ | ❌ 429 硬限 |
| anthropic | 是 | ✅ | ✅ | ❌ 429 硬限（classic 优先） |
| anthropic | 否 | — | 否 | ⚡ extra usage (`client_restriction`) |
| anthropic | 否 | — | ✅ | ❌ 429 硬限 |
| openai/google | 任意 | 否 | 否 | ✅ subscription |
| openai/google | 任意 | ✅ | 否 | ⚡ extra usage (`rate_limited`) |
| openai/google | 任意 | — | ✅ | ❌ 429 硬限 |

### 3.7 Context 键

```go
type ctxKey string
const (
    ctxModel                   ctxKey = "model"
    ctxClientKind              ctxKey = "client_kind"
    ctxSubscriptionEligibility ctxKey = "subscription_eligibility"
    ctxExtraUsageIntent        ctxKey = "extra_usage_intent"
    ctxExtraUsageContext       ctxKey = "extra_usage_context"
)
```

Executor 只读 `ctxExtraUsageContext` 和 `ctxModel`（扣费时需要 `*Model.DefaultCreditRate`）；存在 extra usage context 则请求成功后走扣费钩子。

---

## 四、计费、原子扣费与 Executor 钩子

### 4.1 Credits → fen 转换

```go
// 固定用 catalog default_credit_rate，忽略 plan 折扣覆盖。
// 返回 (cost_fen, credits)；DefaultCreditRate 缺失时返回 (0, 0, err) 由上层决定如何处理。
func computeExtraUsageCostFen(m *types.Model, u types.TokenUsage, creditPriceFen int64) (int64, float64, error) {
    if m == nil || m.DefaultCreditRate == nil {
        return 0, 0, ErrMissingDefaultCreditRate
    }
    rate := m.DefaultCreditRate
    credits := rate.InputRate*float64(u.InputTokens) +
               rate.OutputRate*float64(u.OutputTokens) +
               rate.CacheCreationRate*float64(u.CacheCreationTokens) +
               rate.CacheReadRate*float64(u.CacheReadTokens)
    cost := int64(math.Ceil(credits * float64(creditPriceFen) / 1_000_000))
    if cost < 1 && credits > 0 {
        cost = 1   // ceil 防 sub-cent round-down 到 0
    }
    return cost, credits, nil
}
```

字段名与 `internal/types/request.go` 的 `TokenUsage`（`InputTokens/OutputTokens/CacheCreationTokens/CacheReadTokens`）对齐。`credit_price_fen` 支持运行时热更新（通过 admin API 写 config），无需重启。

### 4.2 原子扣费 SQL（单事务）

```sql
-- 1) 条件扣减
WITH month_spend AS (
  SELECT COALESCE(SUM(-amount_fen), 0) AS spent
  FROM extra_usage_transactions
  WHERE project_id = $1
    AND type = 'deduction'
    -- 月初（Asia/Shanghai 口径）→ 再转回 timestamptz，避免会话时区影响
    AND created_at >= (date_trunc('month', NOW() AT TIME ZONE 'Asia/Shanghai')
                       AT TIME ZONE 'Asia/Shanghai')
)
UPDATE extra_usage_settings s
   SET balance_fen = balance_fen - $2, updated_at = NOW()
  FROM month_spend
 WHERE s.project_id = $1
   AND s.enabled    = TRUE
   AND s.balance_fen >= $2
   AND (s.monthly_limit_fen = 0 OR month_spend.spent + $2 <= s.monthly_limit_fen)
RETURNING balance_fen;

-- 2) 写 ledger（同一事务）
INSERT INTO extra_usage_transactions
  (project_id, type, amount_fen, balance_after_fen, request_id, reason, description)
VALUES ($1, 'deduction', -$2, $3, $4, $5, $6);
```

`UPDATE ... RETURNING` 零行 → 余额/月度/状态校验失败 → 回滚事务并分类错误返回。月度窗口固定 `Asia/Shanghai` 时区（与 plans 计费一致），SQL 中 `date_trunc` 结果再 `AT TIME ZONE 'Asia/Shanghai'` 回到 timestamptz，消除 PostgreSQL 会话时区对边界判定的影响。`GetMonthlyExtraSpendFen` 与用户端 `GET /extra-usage` 使用完全相同的 CTE 口径。

### 4.3 Store 接口

```go
type Store interface {
    // settings
    GetExtraUsageSettings(ctx, projectID) (*ExtraUsageSettings, error)
    UpsertExtraUsageSettings(ctx, *ExtraUsageSettings) error

    // 原子扣费：失败返 ErrInsufficientBalance / ErrMonthlyLimitReached / ErrNotEnabled
    DeductExtraUsage(ctx, DeductReq) (newBalanceFen int64, err error)

    // 充值：settings 行不存在时自动 INSERT ... ON CONFLICT DO UPDATE 创建默认行
    // （enabled=false，balance=amount）。幂等：同一 order_id 二次调用直接返回当前余额。
    TopUpExtraUsage(ctx, TopUpReq) (newBalanceFen int64, err error)

    // 查询
    GetMonthlyExtraSpendFen(ctx, projectID) (int64, error)
    ListExtraUsageTransactions(ctx, projectID, Pagination, TypeFilter)
        ([]ExtraUsageTransaction, int, error)
}

type DeductReq struct {
    ProjectID   string
    AmountFen   int64
    RequestID   string
    Reason      string // "rate_limited" | "client_restriction"
    Description string
}

type TopUpReq struct {
    ProjectID   string
    AmountFen   int64
    OrderID     string
    Reason      string // "user_topup" | "admin_refund"
    Description string
}
```

### 4.4 Executor 扣费钩子

`RequestContext` 要先被 `ResolveModelMW` 或 executor 初始化时填充 `Model *types.Model`（而不只是 model 名字串），这样扣费钩子能直接读 `rc.Model.DefaultCreditRate`。

在 `commitStreamingResponse` / `commitNonStreamingResponse` 已有的 `completeRequest(...)` 之后、`recordMetrics(...)` 之前调用 `settleExtraUsage`：

```go
func (e *Executor) settleExtraUsage(ctx context.Context, rc *RequestContext, usage types.TokenUsage) {
    exc, has := ExtraUsageContextFromContext(ctx)
    if !has {
        return
    }

    costFen, credits, err := computeExtraUsageCostFen(rc.Model, usage, e.cfg.ExtraUsage.CreditPriceFen)
    if err != nil {
        // DefaultCreditRate 缺失：记告警 + 指标，不扣费（管理员修数据后下一单自动恢复）
        e.logger.Error("extra_usage_missing_default_rate",
            "model", rc.Model.Name, "project", rc.ProjectID)
        e.metrics.ExtraUsageMissingRate.Inc()
        return
    }
    rc.IsExtraUsage     = true
    rc.ExtraUsageCostFen = costFen
    rc.ExtraUsageReason = exc.Reason

    newBal, err := e.store.DeductExtraUsage(ctx, DeductReq{
        ProjectID: rc.ProjectID,
        AmountFen: costFen,
        RequestID: rc.RequestID,
        Reason:    exc.Reason,
        Description: fmt.Sprintf("%s | credits=%.2f | model=%s",
            exc.Reason, credits, rc.Model.Name),
    })
    switch {
    case err == nil:
        rc.ExtraUsageBalanceAfterFen = newBal
        e.metrics.ExtraUsageDeductions.WithLabelValues("ok").Inc()
    case errors.Is(err, ErrInsufficientBalance):
        e.logger.Warn("extra_usage_underdraft",
            "project", rc.ProjectID, "cost", costFen)
        e.metrics.ExtraUsageUnderdraft.Inc()
    case errors.Is(err, ErrMonthlyLimitReached):
        e.logger.Warn("extra_usage_monthly_limit_at_settle",
            "project", rc.ProjectID, "cost", costFen)
        e.metrics.ExtraUsageDeductions.WithLabelValues("monthly_limit").Inc()
    default:
        e.logger.Error("extra_usage_deduction_failed", "err", err)
        e.metrics.ExtraUsageDeductions.WithLabelValues("err").Inc()
    }
}
```

### 4.5 写入顺序

先写 `requests` 行（拿到 `request_id`），再扣费并写 ledger（ledger 的 `request_id` 外键指向 requests）。这样即便扣费失败，`requests.is_extra_usage=true` 仍能记录意图，便于审计。

### 4.6 边界场景

| 场景 | 处理 |
|------|------|
| 请求失败（5xx / 上游错误） | 不扣费；`settleExtraUsage` 仅在 status='success' 时触发 |
| 流式请求中途断开 | 已消费 token 照扣（provider transformer 已返回 partial usage） |
| `usage` 数据缺失 | `cost=0`，不写 ledger，记 warning 指标 |
| 余额竞争/并发透支 | 见 §6.1；MVP 接受有界透支，指标触发熔断 |
| `DefaultCreditRate` 缺失 | `settleExtraUsage` 返回 `ErrMissingDefaultCreditRate`，记 `extra_usage_missing_rate_total` 指标，不扣费、不 panic；管理员补数据后下一单恢复 |

---

## 五、API 与配置

### 5.1 用户/项目端 HTTP API

挂在 `/api/v1/projects/{id}/extra-usage/*`，权限与现有 project 资源相同（owner 或 superadmin）。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/extra-usage` | 返回 settings + 当月已消费 + 当前余额 |
| `PUT` | `/extra-usage` | 更新 `enabled`、`monthly_limit_fen`（不允许改 `balance_fen`） |
| `GET` | `/extra-usage/transactions?cursor=&limit=&type=` | 分页 ledger，按 `created_at DESC` |
| `POST` | `/extra-usage/topup` | 创建充值订单，返回支付 URL |
| `GET` | `/extra-usage/topup/{order_id}` | 查询单个充值订单状态 |

**`GET /extra-usage` 响应示例**：

```json
{
  "enabled": true,
  "balance_fen": 23400,
  "monthly_limit_fen": 500000,
  "monthly_spent_fen": 18700,
  "monthly_window_start": "2026-04-01T00:00:00+08:00",
  "credit_price_fen": 5438,
  "updated_at": "2026-04-18T09:21:00+08:00"
}
```

### 5.2 充值流程（复用 `PaymentClient`）

1. `POST /extra-usage/topup`，body：`{"amount_fen": 10000, "channel": "wechat"}`
   - 校验：`min_topup_fen ≤ amount ≤ max_topup_fen`；检查当日累计 < `daily_topup_limit_fen`
2. 插入 `orders` 行：`order_type='extra_usage_topup'`、`plan_id=NULL`、`amount=unit_price=extra_usage_amount_fen=amount_fen`、`status='pending'`
3. 调 `PaymentClient.CreateOrder(...)` 得 `payment_ref` + `payment_url`，更新 orders 行
4. 返回 `payment_url` 给前端
5. 用户完成支付 → 现有 `/api/v1/billing/webhook` 被调用
6. **Webhook handler 改造**：按 `order_type` 分支
   - `subscription` → 现有 `CreateSubscription(...)`（不变）
   - `extra_usage_topup` → `store.TopUpExtraUsage(projectID, amount_fen, order_id, "user_topup")`。若 `settings` 行不存在则 `INSERT ... ON CONFLICT DO UPDATE` 创建默认行（`enabled=false`，用户后续手动开启）

**幂等性**：webhook 已按 `payment_ref` 去重；ledger 上的 `UNIQUE (order_id) WHERE type='topup'` 做第二层保护。

### 5.3 响应头

走 extra usage 成功时响应带：

```
X-Extra-Usage: true
X-Extra-Usage-Reason: rate_limited     # 或 client_restriction
X-Extra-Usage-Cost-Fen: 15
X-Extra-Usage-Balance-Fen: 8500
```

Guard 拒绝时响应带：

```
X-Extra-Usage-Required: true
X-Extra-Usage-Reason: client_restriction
X-Extra-Usage-Enabled: false
X-Extra-Usage-Balance-Fen: 0
```

现有 `X-RateLimit-*` 头保留。

### 5.4 错误响应（HTTP 统一 429）

为最大兼容性，guard 拒绝一律用 HTTP 429（不引入 402），分类信息放在响应头与 `message` 字段。现有 `writeRateLimitError` 的 provider-specific envelope（Anthropic / OpenAI 各自风格）沿用。

| 场景 | `message` 提示 |
|------|------|
| credits 命中 + extra usage 未启用 | "rate limit reached; enable extra usage to continue" |
| credits 命中 + 余额不足 | "rate limit reached; extra usage balance depleted" |
| credits 命中 + 月度上限到顶 | "rate limit reached; extra usage monthly limit reached" |
| 需求 b + 未启用 | "this client cannot use subscription for anthropic models; enable extra usage" |
| 需求 b + 余额不足 | "extra usage balance depleted for this client restriction" |
| 需求 b + 月度到顶 | "extra usage monthly limit reached for this client restriction" |

### 5.5 管理员审计端点（MVP）

```
GET /api/v1/admin/extra-usage/overview
  → 所有启用项目 settings 列表 + 余额 + 近 7 日总扣费
```

项目级 transactions 查询直接复用用户端接口（管理员拥有 superadmin 权限自然可访问任意项目）。

### 5.6 配置 schema

`config.yml` 新增：

```yaml
extra_usage:
  enabled: true               # 全局熔断：false 时所有 guard 按"未开通"拒绝
  credit_price_fen: 5438      # 1M credits = ¥54.38（可运行时调整）
  min_topup_fen: 1000         # 最低充值 ¥10
  max_topup_fen: 200000       # 单次最高充值 ¥2000
  daily_topup_limit_fen: 500000 # 项目每日充值上限 ¥5000
  monthly_window_tz: "Asia/Shanghai"
```

对应 `internal/config/config.go` 的 `Config` 加 `ExtraUsage ExtraUsageConfig`。

### 5.7 前端（MVP）

- 新增页 `/projects/{id}/extra-usage`：
  - 启用开关、月度上限输入
  - 当前余额、当月已消费进度条
  - 充值按钮 → modal 输入金额选渠道 → 跳 `payment_url`
  - Transactions 分页表
- 使用页（`/projects/{id}/usage`）增强：在 credit 用量图右侧加 extra usage 小卡（余额 + 当月消费）
- 首次命中 extra usage 拒绝时，dashboard 顶部 banner 提示可开启

自动充值、低余额提醒等留到后续 phase。

---

## 六、并发、熔断与运维

### 6.1 并发威胁与防御

| 威胁 | 防御 |
|------|------|
| 同项目并发扣费透支 | 见下方"并发透支上界"专项说明 |
| 充值 webhook 重复投递 | webhook 已按 `payment_ref` 去重；ledger `UNIQUE (order_id) WHERE type='topup'` 做第二层 |
| Settings PUT 与扣费冲突 | PUT 只改 `enabled` / `monthly_limit_fen`；`balance_fen` 仅通过 `DeductExtraUsage` / `TopUpExtraUsage` 修改 |
| 缓存读旧余额 | **不缓存 settings**——guard 每次查库（1KB 级查询约 10 ms，可忽略）。QPS 真的成为问题再引入 per-project TTL cache + 写时失效 |
| 月度聚合慢查询 | 索引 `(project_id, type, created_at) WHERE type='deduction'` 覆盖即可；实测不够再考虑 materialized view |

#### 并发透支上界（MVP 诚实说明）

Guard 的 `balance_fen > 0` 判断与 executor 的原子扣费**不在同一事务**：

- Guard 在 t0 读到 `balance=10`，放行请求 A
- Guard 在 t0+ε 读到同样的 `balance=10`（A 还没到 settle），再放行 B、C、…
- t1 时刻 A 上游返回，扣费成功；B/C/… settle 时原子 UPDATE 余额不足，全部 underdraft

**MVP 透支上界 ≈ (项目并发度) × (单次 cost)**，不是"1 次"。在近零余额情形下，如果项目有 N 个并发连接，最坏情况下服务器向上游付出 N 次真实 token 费用却只从用户账户扣回 1 次的量。

**MVP 防御（足够但非零损失）**：

1. 原子扣费 SQL 保证**账户扣款**不会透支成负数（`balance_fen >= $amt` 门槛）
2. Prometheus `extra_usage_underdraft_total` 计数，运维看板监控
3. **自动熔断**：监控脚本 / rule 检测到某项目 `underdraft_total` 在 5 分钟内 > 10 次 → 自动 PATCH `extra_usage_settings.enabled = false`，暂停该项目的 extra usage，给运维人工介入时间
4. 配合 §4.6 里 `DefaultCreditRate` 缺失、`usage` 缺失等不扣费的路径也走同一指标体系

**Phase 2 考虑引入**（超出本 spec）：
- 基于 `max_tokens` 的乐观预扣 + settle 结算差额（完全避免透支，代价是冻结额度）
- 项目级 in-flight 信号量：`in_flight_concurrency ≤ floor(balance_fen / p95_cost)` 动态计算

**结论**：MVP 选择接受有界透支 + 指标熔断。前期用户量低，风险可控；上量后再转 Phase 2 方案。

### 6.2 全局熔断

`extra_usage.enabled: false` → guard 一律按"未开通"拒绝。用于支付故障或计价异常的紧急停用。

### 6.3 迁移与部署顺序

1. 部署前数据准备：确认 catalog 所有模型 `default_credit_rate` 非空
2. 上线 migration `017_extra_usage.sql`（表、列、索引、orders 扩展）
3. 上线新二进制（默认 `extra_usage.enabled: false`）
4. 观察 24h：
   - `SubscriptionEligibilityMW` 日志：非-Claude-Code 请求被标 `client_restriction`（guard 未拦截，走正常链路）
   - 无 5xx、无性能回退
5. 打开 `extra_usage.enabled: true` → 正式生效
6. Dashboard 前端上线（非阻塞）

**Rollback**：`extra_usage.enabled: false` 即刻恢复旧行为。DB migration 对旧代码无破坏（新列均默认值），不需要回滚。

---

## 七、测试计划

### 7.1 单元测试

- `computeExtraUsageCostFen`：零 usage、只有 cache_read、`credits=0.0001 → cost=1`（ceil 行为）
- `DeductExtraUsage`：余额足 / 余额不足 / 月度到顶 / settings 关闭 / project 不存在
- `TopUpExtraUsage`：幂等（同一 order_id 二次调用）
- `SubscriptionEligibilityMW` 决策表（publisher × trace source 笛卡尔积）
- `ExtraUsageGuardMW`：has intent + enabled=false / balance=0 / monthly 超限 / 通过
- `PreCheckResult.LimitType`：credit 命中 → `"credit"`；classic 命中 → `"classic"`；全过 → `""`

### 7.2 集成测试（需 PostgreSQL）

- 并发 20 个请求抢同一 10 fen 余额 → 只允许 `floor(balance/cost)` 个成功，其余收到 429
- 月跨天：扣费发生在 `Asia/Shanghai` 月初 00:00:05 → 归属新月度
- 端到端：开通 → 充值 → 触发需求 b → 响应带 `X-Extra-Usage-*` → ledger 正确 → 余额正确

### 7.3 回归

- 未开 extra usage 的项目：所有现有测试通过（行为与旧版一致）
- `extra_usage.enabled: false`：guard 全拒绝，等同"未开通"

### 7.4 Prometheus 指标与告警

```
extra_usage_requests_total{reason="rate_limited|client_restriction", result="allowed|rejected"}
extra_usage_deductions_total{result="ok|insufficient|monthly_limit|err"}
extra_usage_underdraft_total{project_id}           # 按项目标签，方便熔断
extra_usage_missing_rate_total                     # DefaultCreditRate 缺失
extra_usage_missing_publisher_total                # Model.publisher 为空
extra_usage_balance_fen{project_id}                # gauge
extra_usage_topups_total{channel}
```

**告警规则**（Alertmanager / 运维脚本）：

1. `increase(extra_usage_underdraft_total[5m]) > 10` → 触发自动熔断：PATCH 该项目 `extra_usage_settings.enabled=false`，发送运维通知
2. `increase(extra_usage_missing_rate_total[5m]) > 0` → 告警（数据一致性）
3. `increase(extra_usage_missing_publisher_total[5m]) > 0` → 告警（数据一致性）
4. `extra_usage_balance_fen < monthly_avg_spend / 30` → 低余额 dashboard 提示（Phase 3 再加邮件）

---

## 八、实现阶段拆分

### Phase 1（核心）

1. Migration `017_extra_usage.sql`（表、列、索引、orders 扩展；`publisher` 列 + backfill）
2. `internal/types/extra_usage.go`（types 定义）+ `internal/types/request.go` 加 `ClientKind*` 常量
3. `internal/store/extra_usage.go`（CRUD + 原子扣费 + 充值）
4. `internal/config/config.go` 新增 `ExtraUsageConfig`
5. `internal/ratelimit/composite.go`：`PreCheck` 改为 `PreCheckResult`；新增 `PreCheckClassicOnly`；更新所有调用点
6. `internal/modelcatalog/catalog.go` + `internal/admin/models.go`：`Model.Publisher` 读写 + 非空校验
7. `internal/proxy/trace_middleware.go`：追加 `deriveClientKind`，写入 context
8. `internal/proxy/resolve_model_middleware.go`（从 handler 抽出，填充 `RequestContext.Model *types.Model`）
9. `internal/proxy/subscription_eligibility_middleware.go`
10. `internal/proxy/ratelimit_middleware.go` 改造
11. `internal/proxy/extra_usage_guard_middleware.go`
12. `internal/proxy/executor.go` 加 `settleExtraUsage` 钩子
13. `internal/store/requests.go`：`CompleteRequest` 支持新字段
14. 管理员内部接口 `admin:direct_topup`（不走支付，用于 E2E 测试）
15. Prometheus 指标 + 熔断脚本/rule（§7.4）

### Phase 2（用户可见）

13. 用户端 API（`GET/PUT settings`、`GET transactions`、`POST /topup`、`GET /topup/{id}`）
14. Webhook handler `order_type` 分支
15. 响应头（`X-Extra-Usage-*`）
16. Dashboard extra-usage 设置页 + 使用页增强
17. 管理员 overview 端点

### Phase 3（backlog，不在本 spec 范围）

- 低余额邮件提醒
- 自动充值（签约代扣）
- 管理端审计仪表盘

---

## 九、关键文件变更清单

| 文件 | 变更 | 说明 |
|------|------|------|
| `internal/store/migrations/017_extra_usage.sql` | 新增 | 建表 + 扩列 + backfill publisher |
| `internal/types/extra_usage.go` | 新增 | types |
| `internal/store/extra_usage.go` | 新增 | CRUD + 原子扣费 + 充值 |
| `internal/store/requests.go` | 修改 | 支持 `is_extra_usage` 等新列 |
| `internal/store/orders.go` | 修改 | 支持 `order_type`、nullable plan_id |
| `internal/config/config.go` | 修改 | `ExtraUsageConfig` |
| `internal/ratelimit/composite.go` | 修改 | PreCheck 返回结构体；新增 PreCheckClassicOnly |
| `internal/ratelimit/engine.go` | 修改 | 同上 |
| `internal/modelcatalog/catalog.go` | 修改 | `Model.Publisher` 字段、读写、JSON |
| `internal/types/model.go` | 修改 | 加 `Publisher string` 字段 |
| `internal/types/request.go` | 修改 | 加 `ClientKind*` 常量 + `TokenUsage` 不变 |
| `internal/proxy/trace_middleware.go` | 修改 | 追加 `deriveClientKind`（独立于 TraceSource）|
| `internal/proxy/resolve_model_middleware.go` | 新增 | 从 handler 抽出，写入 `*types.Model` |
| `internal/proxy/subscription_eligibility_middleware.go` | 新增 | 需求 b 判定（读 ClientKind + Publisher） |
| `internal/proxy/ratelimit_middleware.go` | 修改 | 三分支逻辑 |
| `internal/proxy/extra_usage_guard_middleware.go` | 新增 | 通过校验 + intent 写入 |
| `internal/proxy/executor.go` | 修改 | `settleExtraUsage` 钩子 |
| `internal/proxy/handler.go` | 修改 | 去掉已抽出到 resolve_model MW 的代码 |
| `internal/admin/handle_extra_usage.go` | 新增 | 用户端 + admin overview handlers |
| `internal/admin/routes.go` | 修改 | 注册新路由 |
| `internal/admin/models.go` | 修改 | publisher 字段读写 |
| `internal/billing/webhook.go` | 修改 | `order_type` 分支 |
| `dashboard/src/api/extra-usage.ts` | 新增 | API hooks |
| `dashboard/src/pages/extra-usage/*.tsx` | 新增 | 设置页 + 充值 modal + ledger 表 |
| `dashboard/src/pages/usage/UsagePage.tsx` | 修改 | extra usage 小卡 |

---

## 十、参考

- Claude 官方 Extra Usage: https://support.claude.com/en/articles/12429409-manage-extra-usage-for-paid-claude-plans
- 既有相关设计：`docs/design-extra-usage.md`（2026-03-21，已被本 spec 取代）
- 既有相关设计：`docs/superpowers/specs/2026-03-15-redis-rate-limiter-design.md`
- 既有相关设计：`docs/superpowers/specs/2026-03-20-fixed-interval-rate-limit-design.md`
- 既有相关设计：`docs/superpowers/specs/2026-03-21-per-user-credit-quota-design.md`
