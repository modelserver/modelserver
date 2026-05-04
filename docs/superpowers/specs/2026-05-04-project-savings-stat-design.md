# 项目套餐节省指标设计方案

> 状态：草案 · 2026-05-04
>
> 目标：在每个项目的 Overview 页面新增一组指标，对比"按 API 标准计费的等价花费"与"用户实际支出"，量化套餐为用户节省的金额，作为产品价值的可视化体现。

## 一、动机与口径

当前每个项目的 Overview 页只展示 Total Requests / Total Tokens / Total Credits，无法回答"我开这个套餐到底值不值"。本方案在同一页面追加三张 KPI 卡片：

1. **API 标准价**：本次订阅周期内全部用量按各模型 catalog `default_credit_rate`（即官方 API 价）折算后的等价人民币金额。
2. **本周期实付**：当前 active 订阅的 `plan.price_per_period` + 周期内 extra usage 总扣费。
3. **套餐已为您节省**：前者减后者，夹到 `>= 0`；为 0 时第三张卡片改成提示文案"本周期用量较低，套餐尚未回本"。

时间口径统一为 **当前 active subscription 的 `[starts_at, expires_at)` 周期**。该口径与 credit 配额窗口一致，亦最贴近"这个月套餐帮我省了多少"的直觉。无活跃订阅时 `subscription_fen = 0`，"省钱"退化为"API 标准价 vs Extra usage 实付"。

## 二、计费公式

```
api_standard_fen   = Σ_request ( credits_at_catalog_default_rate × credit_price_fen / 1_000_000 )
subscription_fen   = active_plan.price_per_period   (无活跃订阅时为 0)
extra_usage_fen    = Σ_request ( extra_usage_cost_fen )      // 仅 is_extra_usage=true 的请求
actual_paid_fen    = subscription_fen + extra_usage_fen
saved_fen          = max(0, api_standard_fen − actual_paid_fen)
```

要点：

- `credit_price_fen` 复用 `config.ExtraUsage.CreditPriceFen`（默认 5438，即 ¥54.38/1M credits）。
- Extra usage 请求按设计本来就以 catalog default 计价，因此被 `api_standard_fen` 覆盖，并以同等金额体现在 `extra_usage_fen` 中，对节省贡献为 0 —— 符合"加油包不省钱"直觉。
- **v1 不应用 long-context 倍率**。仅 OpenAI 系少数模型受影响，按基础 `InputRate / OutputRate` 计算。预计误差 < 5%。已知限制，留 v2 改进。
- 模型不在 catalog 或 `DefaultCreditRate == nil` 时跳过该模型并打印一条 warn 日志（`model=... project=... reason=missing_default_credit_rate`），不阻断指标返回。

## 三、后端实现

### 3.1 不改 schema

`requests` 表 schema 保持不变。所有指标查询时实时聚合，避免 migration / backfill。catalog 默认费率事后变化会同步影响历史展示数字，文档中标注。

### 3.2 Store 层（`internal/store/usage.go`）

新增两个聚合方法：

```go
// PerModelTokenSums 是 GetPerModelTokenSums 的输出元素。
type PerModelTokenSums struct {
    Model               string
    RequestCount        int64
    InputTokens         int64
    OutputTokens        int64
    CacheCreationTokens int64
    CacheReadTokens     int64
}

// GetPerModelTokenSums 返回指定项目在 [since, until) 内按 model 聚合的 token 总数。
func (s *Store) GetPerModelTokenSums(projectID string, since, until time.Time) ([]PerModelTokenSums, error)

// GetExtraUsageSpendInWindow 返回指定项目在 [since, until) 内 is_extra_usage=true
// 请求的 extra_usage_cost_fen 总和。
func (s *Store) GetExtraUsageSpendInWindow(projectID string, since, until time.Time) (int64, error)
```

SQL 形态：

```sql
-- GetPerModelTokenSums
SELECT model,
       COUNT(*),
       COALESCE(SUM(input_tokens),         0),
       COALESCE(SUM(output_tokens),        0),
       COALESCE(SUM(cache_creation_tokens),0),
       COALESCE(SUM(cache_read_tokens),    0)
FROM requests
WHERE project_id = $1 AND created_at >= $2 AND created_at < $3
GROUP BY model;

-- GetExtraUsageSpendInWindow
SELECT COALESCE(SUM(extra_usage_cost_fen), 0)
FROM requests
WHERE project_id = $1 AND created_at >= $2 AND created_at < $3
  AND is_extra_usage = TRUE;
```

### 3.3 计算辅助（`internal/billing/savings.go`）

纯函数，方便单测覆盖所有边界。

```go
type CostBreakdown struct {
    APIStandardFen   int64     `json:"api_standard_fen"`
    SubscriptionFen  int64     `json:"subscription_fen"`
    ExtraUsageFen    int64     `json:"extra_usage_fen"`
    ActualPaidFen    int64     `json:"actual_paid_fen"`
    SavedFen         int64     `json:"saved_fen"`
    PeriodStart      time.Time `json:"period_start"`
    PeriodEnd        time.Time `json:"period_end"`
    HasActiveSub     bool      `json:"has_active_subscription"`
}

// ComputeCostBreakdown 拼装最终展示数据。catalog 用于查 model 的 default rate；
// sub 与 plan 可为 nil（表示无活跃订阅）。
func ComputeCostBreakdown(
    sums []store.PerModelTokenSums,
    extraUsageFen int64,
    catalog modelcatalog.Catalog,
    creditPriceFen int64,
    sub *types.Subscription,
    plan *types.Plan,
) CostBreakdown
```

实现要点：

1. 遍历 `sums`，对每行 `m, ok := catalog.Lookup(s.Model)`；`!ok || m.DefaultCreditRate == nil` → warn 日志 + 跳过。
2. `credits = rate.InputRate*Input + rate.OutputRate*Output + rate.CacheCreationRate*CacheCreation + rate.CacheReadRate*CacheRead`（不应用 long-context multiplier）。
3. `apiFen += int64(math.Ceil(credits * creditPriceFen / 1_000_000))`，单行 ceil 避免少算。
4. `SubscriptionFen = plan.PricePerPeriod`（仅当 sub != nil 且 plan != nil 时），否则 0。
5. `ActualPaidFen = SubscriptionFen + ExtraUsageFen`；`SavedFen = max(0, APIStandardFen − ActualPaidFen)`。
6. `PeriodStart / PeriodEnd` 来自 sub.StartsAt / sub.ExpiresAt；无活跃订阅时取调用方传入的 fallback 窗口（默认本月 UTC）。

### 3.4 Endpoint 接入

复用现有 `GET /api/v1/admin/projects/:id/usage/overview`（在 `internal/admin/handle_requests.go` 中调用 `GetUsageOverview`）。改动：

- handler 内部解析 `since/until`：
  - 若 caller 没传或显式传 `period=current_subscription`，则取 active subscription 周期，并附带 `cost_breakdown`。
  - 若 caller 传任意其他窗口，则不计算 `cost_breakdown`（避免订阅金额跨窗口失真），仅返回原有字段。
- 响应 JSON 新增可选字段：

```json
{
  "request_count": 1234,
  "total_tokens":  98765,
  "total_credits_k": 12,
  "since": "...",
  "until": "...",
  "cost_breakdown": {
    "api_standard_fen": 32845,
    "subscription_fen": 19900,
    "extra_usage_fen": 0,
    "actual_paid_fen": 19900,
    "saved_fen": 12945,
    "period_start": "...",
    "period_end": "...",
    "has_active_subscription": true
  }
}
```

handler 通过依赖注入拿到 `catalog modelcatalog.Catalog`、`cfg.ExtraUsage.CreditPriceFen`、`store.GetActiveSubscription` + `store.GetPlan`。

## 四、前端实现

`dashboard/src/pages/dashboard/OverviewPage.tsx` 的 StatCards 区域：

```
┌──────────────┬──────────────┬──────────────┐
│ Total        │ Total Tokens │ Total Credits│   现有
│ Requests     │              │              │
└──────────────┴──────────────┴──────────────┘
┌──────────────┬──────────────┬──────────────┐
│ API 标准价    │ 本周期实付    │ 套餐已为您节省 │   新增
│ ¥328.45      │ ¥199.00      │ ¥129.45      │
│ 按官方定价    │ 订阅 ¥199    │ ↑ 65% off    │
│ 折算          │ + 加油包 ¥0  │              │
└──────────────┴──────────────┴──────────────┘
```

- 数据源：`useUsageOverview(projectId)` 返回值的新字段 `cost_breakdown`。
- 当 `cost_breakdown == null`（无活跃订阅且后端没返回，或非订阅周期窗口）时，三张新卡不渲染。
- 当 `saved_fen <= 0`：第三张卡变灰色，显示文案"本周期用量较低，套餐尚未回本"。
- 卡片下方 hover tooltip 显示完整公式与周期起止时间，避免误解。
- 金额格式：`¥X,XXX.XX`（fen / 100，保留两位小数）。
- 类型补充在 `dashboard/src/api/usage.ts` 的 `UsageOverview` 中追加 `cost_breakdown?: CostBreakdown`。

## 五、配置与依赖

- 复用 `config.ExtraUsage.CreditPriceFen`，无新增配置项。
- catalog 通过现有 `*App` / handler 依赖注入获得（已在 admin 路由初始化中可用）。
- 不需要新表、不需要 migration。

## 六、测试

新增 `internal/billing/savings_test.go`，表格驱动覆盖：

| 场景 | 期望 |
|------|------|
| 普通付费套餐，用量充足 | `saved_fen > 0`，`api_standard > actual_paid` |
| Free 套餐（`subscription_fen = 0`） | `actual_paid = extra_usage`，`saved = max(0, api_standard − extra_usage)` |
| 用量极少（`api_standard < subscription`） | `saved_fen` 被夹到 0，`HasActiveSub = true` |
| 模型不在 catalog | 跳过该模型，warn 日志，其他模型正常累计 |
| 模型 `DefaultCreditRate == nil` | 同上跳过 |
| 包含 extra usage 与套餐内请求混合 | `extra_usage_fen` 只统计 `is_extra_usage=true` 的部分 |
| 无活跃订阅 | `subscription_fen = 0`，`HasActiveSub = false`，period 用 fallback 窗口 |

`internal/store/usage_test.go` 增加 `GetPerModelTokenSums` 与 `GetExtraUsageSpendInWindow` 的集成测试（参照已有 store 测试模式）。

handler 层做一个最小集成测试：注入 fake catalog + 内置项目数据，断言 `cost_breakdown` 字段存在且数值正确。

## 七、已知限制 / v2 候选

1. **Long-context 倍率未参与**。误差 < 5%。v2 可在 `GetPerModelTokenSums` 之外加 `(model, total_input_bucket)` 二级聚合，或改为流式按行计算。
2. **catalog 费率改动会回溯影响历史展示**。如果产品后期希望"展示当时的官方价"，再加 `requests.api_standard_credits` 列做快照。
3. **多订阅周期对比**未提供。当前仅展示本周期，"上周期省了多少"留作后续。
4. **跨项目汇总**未提供（用户在 dashboard 首页的多项目总省钱）。需要时按相同公式聚合即可。

## 八、实施顺序建议

1. `internal/store/usage.go` 加两个聚合方法 + 单测。
2. `internal/billing/savings.go` + 单测（不依赖 store，纯函数）。
3. `internal/admin/handle_requests.go` 接入，注入 catalog/config/sub-plan 查询。
4. `dashboard/src/api/usage.ts` 类型 + `OverviewPage.tsx` 三张卡片。
5. 端到端联调（开发环境真实数据 + 一个 free / 一个付费项目）。
