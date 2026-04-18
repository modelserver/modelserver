# Extra Usage 设计方案

> **⚠️ 已废弃（2026-04-18）**：本文档被 `docs/superpowers/specs/2026-04-18-extra-usage-design.md` 取代。
> 新方案覆盖了原方案未考虑的"客户端限制"场景（非 Claude Code 客户端访问 Claude 系列模型强制走 extra usage），
> 并对中间件分层、计费口径（catalog `default_credit_rate` 而非 plan 覆盖）、orders 表扩展等做了调整。
> 本文档仅作历史参考。

## 一、概述

当用户的套餐 credit 窗口用量达到上限时，目前系统直接返回 `429 rate_limit_error`。Extra Usage 功能允许付费套餐用户在超限后继续使用，超出部分按 API 标准费率从预充值余额中扣费。

参考 Claude 官方 Extra Usage 模型：Opt-in 启用 → 预充值 → 设月度上限 → 超限后自动从余额扣费。

## 二、核心概念

### 2.1 计费公式

现有 credit 体系已经映射了 API 定价：

```
credit_rate = API_price_per_MTok / 7.5
credits = tokens × credit_rate
```

因此 **1M credits = $7.50 USD**。转换为 CNY（可配置汇率）：

```
extra_usage_cost_fen = ceil(credits_consumed × credit_price_fen / 1,000,000)
```

系统配置默认值：`credit_price_fen = 5438`（即 ¥54.38 / 1M credits，按 $7.50 × 7.25 汇率）

### 2.2 适用范围

- 所有套餐（包括 Free）均可启用 Extra Usage
- 仅 credit 规则触发的限速走 Extra Usage；Classic 规则（RPM/TPM 等）仍然硬限

## 三、数据库设计

### 3.1 新表：`extra_usage_settings`

```sql
CREATE TABLE extra_usage_settings (
    project_id       UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled          BOOLEAN NOT NULL DEFAULT FALSE,
    balance          BIGINT  NOT NULL DEFAULT 0,     -- 余额，单位：分 (CNY fen)
    monthly_limit    BIGINT  NOT NULL DEFAULT 0,     -- 月度消费上限，0=不限
    auto_reload      BOOLEAN NOT NULL DEFAULT FALSE,
    reload_threshold BIGINT  NOT NULL DEFAULT 0,     -- 余额低于此值时自动充值
    reload_amount    BIGINT  NOT NULL DEFAULT 0,     -- 自动充值金额
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### 3.2 新表：`extra_usage_transactions`

```sql
CREATE TABLE extra_usage_transactions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,         -- 'topup', 'deduction', 'auto_reload'
    amount      BIGINT NOT NULL,       -- 正数=充值, 负数=扣费
    balance_after BIGINT NOT NULL,     -- 交易后余额
    description TEXT NOT NULL DEFAULT '',
    request_id  UUID,                  -- 扣费时关联的请求 ID
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_extra_usage_tx_project ON extra_usage_transactions(project_id, created_at);
CREATE INDEX idx_extra_usage_tx_monthly ON extra_usage_transactions(project_id, type, created_at);
```

### 3.3 Migration 文件

新增 `005_extra_usage.sql`。

## 四、后端设计

### 4.1 新增类型 (`internal/types/extra_usage.go`)

```go
type ExtraUsageSettings struct {
    ProjectID       string    `json:"project_id"`
    Enabled         bool      `json:"enabled"`
    Balance         int64     `json:"balance"`          // fen
    MonthlyLimit    int64     `json:"monthly_limit"`    // fen, 0=unlimited
    AutoReload      bool      `json:"auto_reload"`
    ReloadThreshold int64     `json:"reload_threshold"` // fen
    ReloadAmount    int64     `json:"reload_amount"`    // fen
    CreatedAt       time.Time `json:"created_at"`
    UpdatedAt       time.Time `json:"updated_at"`
}

type ExtraUsageTransaction struct {
    ID           string    `json:"id"`
    ProjectID    string    `json:"project_id"`
    Type         string    `json:"type"`         // topup, deduction, auto_reload
    Amount       int64     `json:"amount"`        // positive=credit, negative=debit
    BalanceAfter int64     `json:"balance_after"`
    Description  string    `json:"description"`
    RequestID    string    `json:"request_id,omitempty"`
    CreatedAt    time.Time `json:"created_at"`
}
```

### 4.2 新增 Store 方法 (`internal/store/extra_usage.go`)

| 方法 | 说明 |
|------|------|
| `GetExtraUsageSettings(projectID)` | 获取设置，不存在返回 nil |
| `UpsertExtraUsageSettings(settings)` | 创建/更新设置 |
| `DeductExtraUsageBalance(projectID, amountFen, requestID, desc)` | **原子扣费**：`UPDATE ... SET balance = balance - $1 WHERE balance >= $1 RETURNING balance`，失败表示余额不足 |
| `TopUpExtraUsageBalance(projectID, amountFen, desc)` | 充值并记录交易 |
| `GetMonthlyExtraSpend(projectID)` | `SUM(-amount) FROM extra_usage_transactions WHERE type='deduction' AND created_at >= month_start` |
| `ListExtraUsageTransactions(projectID, pagination)` | 分页查询交易记录 |

`DeductExtraUsageBalance` 关键实现：

```go
func (s *Store) DeductExtraUsageBalance(projectID string, amountFen int64, requestID, desc string) (int64, error) {
    ctx := context.Background()
    tx, _ := s.pool.Begin(ctx)

    // 原子扣减，WHERE balance >= amount 保证不透支
    var newBalance int64
    err := tx.QueryRow(ctx, `
        UPDATE extra_usage_settings
        SET balance = balance - $1, updated_at = NOW()
        WHERE project_id = $2 AND enabled = TRUE AND balance >= $1
        RETURNING balance`, amountFen, projectID,
    ).Scan(&newBalance)
    if err != nil {
        tx.Rollback(ctx)
        return 0, fmt.Errorf("insufficient balance or not enabled")
    }

    // 记录交易
    tx.Exec(ctx, `
        INSERT INTO extra_usage_transactions (project_id, type, amount, balance_after, description, request_id)
        VALUES ($1, 'deduction', $2, $3, $4, $5)`,
        projectID, -amountFen, newBalance, desc, nullString(requestID))

    tx.Commit(ctx)
    return newBalance, nil
}
```

### 4.3 核心流程改造：Rate Limit Middleware

**当前流程：**

```
PreCheck → allowed=false → 返回 429
```

**改造后流程：**

```
PreCheck → allowed=false → 检查是否 credit 规则触发
  → 是 credit 规则 + extra usage 已启用 + 余额 > 0 + 月度未超限
    → 放行请求，在 context 中标记 extraUsage=true
    → 请求完成后，计算实际 cost → 扣费
  → 否则 → 返回 429（但 response body 中增加 extra_usage_available 提示）
```

#### 4.3.1 修改 `CompositeRateLimiter.PreCheck`

返回值增加 `limitType string`，区分 `"credit"` 和 `"classic"`：

```go
func (c *CompositeRateLimiter) PreCheck(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy) (bool, time.Duration, string, error) {
    // ...
    // credit rules check:
    if used >= float64(rule.MaxCredits) {
        return false, retryAfter, "credit", nil
    }
    // classic rules check:
    if !allowed {
        return false, retryAfter, "classic", nil
    }
    return true, 0, "", nil
}
```

#### 4.3.2 修改 Rate Limit Middleware (`ratelimit_middleware.go`)

```go
if !allowed {
    if limitType == "credit" {
        settings, _ := st.GetExtraUsageSettings(project.ID)
        if settings != nil && settings.Enabled && settings.Balance > 0 {
            if settings.MonthlyLimit == 0 || monthlySpendOK(st, project.ID, settings.MonthlyLimit) {
                // 放行，标记为 extra usage
                ctx := context.WithValue(r.Context(), ctxExtraUsage, true)
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }
        }
    }
    writeRateLimitError(w, retryAfter)
    return
}
```

#### 4.3.3 Executor 完成请求后扣费

在 `executor.go` 中完成请求记录后，检查 `extraUsage` context flag：

```go
if isExtraUsage(reqCtx) {
    costFen := computeExtraUsageCost(credits, creditPriceFen)
    newBalance, err := store.DeductExtraUsageBalance(projectID, costFen, requestID,
        fmt.Sprintf("model:%s credits:%.0f", model, credits))
    if err != nil {
        logger.Warn("extra usage deduction failed", "error", err)
    }
}
```

#### 4.3.4 Request 记录增加 extra_usage 标记

`requests` 表添加：

```sql
ALTER TABLE requests ADD COLUMN is_extra_usage BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE requests ADD COLUMN extra_usage_cost BIGINT NOT NULL DEFAULT 0;
```

`Request` struct 添加：

```go
IsExtraUsage    bool    `json:"is_extra_usage"`
ExtraUsageCost  int64   `json:"extra_usage_cost"` // fen
```

### 4.4 新增 API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/v1/projects/{id}/extra-usage` | 获取 Extra Usage 设置与余额 |
| `PUT` | `/api/v1/projects/{id}/extra-usage` | 更新设置（启用/禁用、月度上限、自动充值） |
| `POST` | `/api/v1/projects/{id}/extra-usage/topup` | 充值（走支付流程或管理员直充） |
| `GET` | `/api/v1/projects/{id}/extra-usage/transactions` | 查询交易记录（分页） |
| `GET` | `/api/v1/projects/{id}/extra-usage/monthly-spend` | 查询当月已消费 |

### 4.5 充值流程

充值复用现有支付流程（PaymentClient）：

1. 用户提交充值金额 + 支付渠道
2. 创建充值订单（复用 `orders` 表，新增 `order_type` 字段区分套餐订单和充值订单；或新建 `extra_usage_orders` 表）
3. 调用 PaymentClient 创建支付
4. 支付成功 webhook → `TopUpExtraUsageBalance()`
5. 可选：管理员直接给项目充值（无需支付）

### 4.6 自动充值

MVP 阶段采用方案 A：用户手动充值，设置低余额提醒阈值，系统在余额低于阈值时在 dashboard 和 API 响应头中提醒。

后续 Phase 可接入自动扣款能力（微信/支付宝签约代扣）。

### 4.7 配置

`config.yml` 新增：

```yaml
extra_usage:
  enabled: true                    # 全局开关
  credit_price_fen: 5438           # 每 1M credits 的 CNY 费用（分）
  min_topup_fen: 1000              # 最低充值金额 ¥10
  max_topup_fen: 200000            # 单次最大充值 ¥2000
  daily_topup_limit_fen: 200000    # 每日充值上限 ¥2000
```

## 五、前端设计

### 5.1 Extra Usage 设置页面

位置：`/projects/{id}/settings` 或新增独立 tab `/projects/{id}/extra-usage`

功能：

- 启用/禁用 Extra Usage 开关
- 当前余额显示
- 充值按钮 → 输入金额 + 选择支付渠道 → 跳转支付
- 月度消费上限设置
- 低余额提醒阈值设置
- 交易记录列表（分页）

### 5.2 Usage Dashboard 增强

- 在 credit 用量条形图上增加 "Extra Usage" 区域（超过 100% 的部分用不同颜色标注）
- 当用户正在使用 Extra Usage 时，显示明确提示
- 显示当月 Extra Usage 花费

### 5.3 Rate Limit 提示增强

- 未启用 Extra Usage：提示 "已达限速上限，可启用 Extra Usage 继续使用"
- 余额不足：提示 "Extra Usage 余额不足，请充值"

## 六、API 响应头增强

在代理请求的响应中添加信息头，供客户端感知状态：

```
X-RateLimit-Credit-Used: 45000
X-RateLimit-Credit-Limit: 55000
X-RateLimit-Credit-Remaining: 10000
X-Extra-Usage: true              // 本次请求为 Extra Usage
X-Extra-Usage-Cost: 15           // 本次扣费（分）
X-Extra-Usage-Balance: 8500      // 剩余余额（分）
```

## 七、安全与边界处理

| 场景 | 处理方式 |
|------|---------|
| 请求已发出但扣费失败（余额刚好不足） | 请求已完成无法撤回，记录欠费日志，下次请求前拒绝 |
| 并发请求导致余额竞争 | `UPDATE ... WHERE balance >= amount` 原子操作保证不透支 |
| 套餐过期降为 Free | Extra Usage 设置和余额保留，用户仍可使用 |
| 月中更改月度上限 | 立即生效，按新上限判断 |
| 充值退款 | 管理员手动操作，插入 `refund` 类型交易 |

## 八、实现优先级

### Phase 1（MVP）

1. 数据库 migration（settings + transactions 表，requests 表加字段）
2. types 定义
3. store 层 CRUD + 原子扣费
4. config 新增 ExtraUsageConfig
5. rate limit middleware 改造（核心逻辑）
6. executor 扣费 hook
7. 管理员直充 API（绕过支付，用于测试）
8. 基础 API 端点（GET/PUT settings, GET transactions, GET monthly-spend）

### Phase 2

9. 前端 Extra Usage 设置页面
10. 充值支付流程（复用 PaymentClient）
11. dashboard usage 页面增强
12. 响应头添加

### Phase 3

13. 低余额提醒（邮件/dashboard 通知）
14. 自动充值（签约代扣）
15. 管理后台查看所有项目 Extra Usage 状态

## 九、关键文件变更清单

| 文件 | 变更类型 | 说明 |
|------|---------|------|
| `internal/store/migrations/005_extra_usage.sql` | 新增 | 建表 migration |
| `internal/types/extra_usage.go` | 新增 | 类型定义 |
| `internal/store/extra_usage.go` | 新增 | 数据库操作 |
| `internal/config/config.go` | 修改 | 新增 ExtraUsageConfig |
| `internal/ratelimit/engine.go` | 修改 | PreCheck 返回 limitType |
| `internal/ratelimit/composite.go` | 修改 | PreCheck 区分 credit/classic |
| `internal/proxy/ratelimit_middleware.go` | 修改 | Extra Usage 放行逻辑 |
| `internal/proxy/auth_middleware.go` | 修改 | 加载 ExtraUsageSettings 到 context |
| `internal/proxy/executor.go` | 修改 | 请求完成后扣费 |
| `internal/admin/handle_extra_usage.go` | 新增 | API handlers |
| `internal/admin/routes.go` | 修改 | 注册新路由 |
| `internal/types/request.go` | 修改 | 添加 IsExtraUsage 字段 |
| `internal/store/requests.go` | 修改 | CreateRequest/CompleteRequest 支持新字段 |
| `dashboard/src/api/types.ts` | 修改 | 新增 TS 类型 |
| `dashboard/src/api/extra-usage.ts` | 新增 | API hooks |
| `dashboard/src/pages/extra-usage/` | 新增 | 前端页面 |
