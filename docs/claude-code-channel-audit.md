# Claude Code Channel 实现审计报告

> 审计日期: 2026-03-15
> Claude Code CLI 版本: 2.1.76 (binary at `/root/.local/share/claude/versions/2.1.76`)
> Modelserver 分支: main (commit b9b6868)

## 概述

本报告对比 Claude Code CLI v2.1.76 的实际行为与 modelserver 中 Claude Code channel 的代理实现，
覆盖 OAuth 流程、API 请求 headers、token 刷新逻辑等方面。

---

## 1. OAuth 流程

### 1.1 OAuth 端点和 Client ID

| 配置项 | CLI 实际值 | Modelserver 值 | 一致性 |
|--------|-----------|---------------|--------|
| Token URL | `https://platform.claude.com/v1/oauth/token` | `https://platform.claude.com/v1/oauth/token` | ✅ 一致 |
| Auth URL | `https://claude.ai/oauth/authorize` | `https://claude.ai/oauth/authorize` | ✅ 一致 |
| Client ID | `9d1c250a-e61b-44d9-88ed-5944d1962f5e` | `9d1c250a-e61b-44d9-88ed-5944d1962f5e` | ✅ 一致 |
| Scopes | `user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload` | 同左 | ✅ 一致 |

### 1.2 PKCE 流程

| 参数 | CLI 行为 | Modelserver 行为 | 一致性 |
|------|---------|-----------------|--------|
| code_verifier | 64 bytes random, base64url | 64 bytes random, base64url | ✅ 一致 |
| code_challenge | SHA256(verifier), base64url | SHA256(verifier), base64url | ✅ 一致 |
| code_challenge_method | S256 | S256 | ✅ 一致 |
| state | 随机 bytes, base64url | 32 bytes random, base64url | ✅ 一致 |

### 1.3 Token Exchange

| 参数 | CLI 行为 | Modelserver 行为 | 一致性 |
|------|---------|-----------------|--------|
| grant_type | `authorization_code` | `authorization_code` | ✅ 一致 |
| 包含 state | 是 | 是 | ✅ 一致 |

### 1.4 Token Refresh

| 参数 | CLI 行为 | Modelserver 行为 | 一致性 |
|------|---------|-----------------|--------|
| grant_type | `refresh_token` | `refresh_token` | ✅ 一致 |
| 包含 scope | 是 | 是 | ✅ 一致 |
| 提前刷新时间 | 5 分钟 (300s) | 5 分钟 (300s) | ✅ 一致 |
| 防并发机制 | CLI 内部单实例 | singleflight | ✅ 等效 |

**结论: OAuth 流程实现正确，与 CLI 一致。**

---

## 2. HTTP Headers 对比（LLM API 请求）

### 2.1 核心 Anthropic Headers

| Header | CLI 实际值 | Modelserver 值 | 一致性 | 严重程度 |
|--------|-----------|---------------|--------|---------|
| `Authorization` | `Bearer {token}` | `Bearer {token}` | ✅ 一致 | - |
| `Anthropic-Version` | `2023-06-01` | `2023-06-01` | ✅ 一致 | - |
| `Anthropic-Dangerous-Direct-Browser-Access` | `true` | `true` | ✅ 一致 | - |
| `X-App` | `cli` | `cli` | ✅ 一致 | - |

### 2.2 Anthropic-Beta Header ❌ 有差异

**Modelserver 当前值:**
```
claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14
```

**CLI v2.1.76 的 beta 常量定义:**
```javascript
// 定义的所有 beta 常量
"claude-code-20250219"              // 核心 Claude Code 标识
"interleaved-thinking-2025-05-14"   // 交错思考
"context-1m-2025-08-07"             // 1M 上下文
"context-management-2025-06-27"     // 上下文管理
"structured-outputs-2025-12-15"     // 结构化输出
"web-search-2025-03-05"             // 网页搜索
"tool-examples-2025-10-29"          // 工具示例
"advanced-tool-use-2025-11-20"      // 高级工具使用
"tool-search-tool-2025-10-19"       // 工具搜索
"effort-2025-11-24"                 // 推理努力控制
"prompt-caching-scope-2026-01-05"   // 提示缓存范围
"fast-mode-2026-02-01"              // 快速模式
"redact-thinking-2026-02-12"        // 思考编辑
"afk-mode-2026-01-31"              // AFK 模式
```

**CLI 中定义的两个关键 beta 集合:**
```javascript
// "必需" beta (用于 API 请求):
JHA = Set(["claude-code-20250219", "interleaved-thinking-2025-05-14", "context-management-2025-06-27"])

// "扩展" beta (用于某些场景):
XHA = Set(["interleaved-thinking-2025-05-14", "context-1m-2025-08-07", "tool-search-tool-2025-10-19", "tool-examples-2025-10-29"])
```

**CLI 中 OAuth 认证专用 beta:**
```javascript
uj = "oauth-2025-04-20"  // 仅用于非 API 调用（如 client_data 请求）
```

**差异分析:**

| Beta Flag | CLI 是否使用 | Modelserver 是否使用 | 问题 |
|-----------|-------------|---------------------|------|
| `claude-code-20250219` | ✅ (核心) | ✅ | 一致 |
| `oauth-2025-04-20` | ❌ 仅用于非 API 调用 | ✅ 包含在 API 请求中 | ⚠️ 不影响功能但不一致 |
| `interleaved-thinking-2025-05-14` | ✅ (核心) | ✅ | 一致 |
| `fine-grained-tool-streaming-2025-05-14` | ❌ **不存在于 CLI 中** | ✅ 包含在 API 请求中 | ❌ **已过时/无效** |
| `context-management-2025-06-27` | ✅ (核心) | ❌ 缺失 | ❌ **缺失关键 beta** |

**建议修改 `Anthropic-Beta` 为:**

CLI 中 beta 是动态组合的，但基础集合 (JHA) 是:
```
claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27
```

根据请求内容的不同，CLI 还会动态添加其他 beta（如 `context-1m-2025-08-07`, `structured-outputs-2025-12-15` 等），
但 modelserver 作为代理无法预知用户请求需要哪些 beta。

**推荐的最小一致集合:**
```
claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27
```

如果需要支持更多特性，可以扩展为（包括所有可能被用到的 beta）:
```
claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,context-1m-2025-08-07,structured-outputs-2025-12-15,prompt-caching-scope-2026-01-05
```

### 2.3 User-Agent ❌ 版本过时

| 项目 | CLI 实际值 | Modelserver 值 | 问题 |
|------|-----------|---------------|------|
| User-Agent | `claude-cli/2.1.76 (external, cli)` | `claude-cli/1.0.83 (external, cli)` | ❌ **版本严重过时** |

CLI 的 User-Agent 格式为:
```
claude-cli/{VERSION} (external, {ENTRYPOINT}{agent_sdk}{client_app}{workload})
```
其中 VERSION 来自编译时常量，ENTRYPOINT 通常为 `cli`。

**建议: 更新为 `claude-cli/2.1.76 (external, cli)` 或更高版本。**

### 2.4 X-Stainless SDK Headers ❌ 多个值过时

这些 headers 由 Anthropic JS SDK 自动生成，反映实际运行环境。

| Header | CLI 实际值 (v2.1.76) | Modelserver 值 | 问题 |
|--------|---------------------|---------------|------|
| `X-Stainless-Lang` | `js` | `js` | ✅ 一致 |
| `X-Stainless-Package-Version` | `0.74.0` | `0.52.0` | ❌ **SDK 版本过时** |
| `X-Stainless-OS` | `Linux` | `Linux` | ✅ 一致 |
| `X-Stainless-Runtime` | `bun` | `node` | ❌ **运行时错误** (CLI 使用 Bun, 非 Node.js) |
| `X-Stainless-Runtime-Version` | `1.3.11` | `v22.13.1` | ❌ **版本错误** (应为 Bun 版本) |
| `X-Stainless-Arch` | `x64` | `x64` | ✅ 一致 |

**说明:** Claude Code v2.1.76 使用 Bun 运行时（v1.3.11）编译为原生二进制。SDK 版本为 `@anthropic-ai/sdk@0.74.0`。

### 2.5 其他 Headers

| Header | CLI 行为 | Modelserver 行为 | 一致性 |
|--------|---------|-----------------|--------|
| `Connection` | 由 HTTP 层管理 | 手动设为 `keep-alive` | ⚠️ 无害但多余 |
| `Accept-Encoding` | SDK 默认 | 主动删除 | ✅ 合理（代理控制压缩） |
| `X-Forwarded-For` | 不发送 | 主动抑制 | ✅ 合理 |

### 2.6 Query Parameter

| 参数 | CLI 行为 | Modelserver 行为 | 一致性 |
|------|---------|-----------------|--------|
| `?beta=true` | SDK beta 资源自动添加 | 手动添加 | ✅ 一致 |

---

## 3. 问题汇总与优先级

### 🔴 高优先级（可能影响功能或被服务端检测）

1. **`Anthropic-Beta` 包含不存在的 beta `fine-grained-tool-streaming-2025-05-14`**
   - 文件: `internal/proxy/claudecode.go:42`
   - 该 beta 不存在于 CLI v2.1.76 的代码中，可能是早期版本遗留
   - 风险: 服务端可能忽略未知 beta，但也可能触发异常行为

2. **`Anthropic-Beta` 缺失核心 beta `context-management-2025-06-27`**
   - 文件: `internal/proxy/claudecode.go:42`
   - 这是 CLI 基础 beta 集合 (JHA) 的成员
   - 风险: 缺失该 beta 可能导致部分上下文管理功能不可用

3. **`User-Agent` 版本严重过时 (`1.0.83` → 应为 `2.1.76`)**
   - 文件: `internal/proxy/claudecode.go:46`
   - 风险: 服务端可能基于版本号进行功能门控或行为调整

### 🟡 中优先级（不一致但短期不太可能导致问题）

4. **`X-Stainless-Package-Version` 过时 (`0.52.0` → 应为 `0.74.0`)**
   - 文件: `internal/proxy/claudecode.go:48`

5. **`X-Stainless-Runtime` 错误 (`node` → 应为 `bun`)**
   - 文件: `internal/proxy/claudecode.go:50`

6. **`X-Stainless-Runtime-Version` 错误 (`v22.13.1` → 应为 `1.3.11`)**
   - 文件: `internal/proxy/claudecode.go:51`

7. **`Anthropic-Beta` 中 `oauth-2025-04-20` 不应出现在 API 请求中**
   - 该 beta 在 CLI 中仅用于非 API 调用 (client_data 等)

### 🟢 低优先级（设计差异，可接受）

8. **`Connection: keep-alive` 是多余的**（HTTP/1.1 默认行为）

---

## 4. 建议修改

### `internal/proxy/claudecode.go` 第 42-52 行:

```go
// 修改前:
req.Header.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
req.Header.Set("User-Agent", "claude-cli/1.0.83 (external, cli)")
req.Header.Set("X-Stainless-Package-Version", "0.52.0")
req.Header.Set("X-Stainless-Runtime", "node")
req.Header.Set("X-Stainless-Runtime-Version", "v22.13.1")

// 修改后:
req.Header.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27")
req.Header.Set("User-Agent", "claude-cli/2.1.76 (external, cli)")
req.Header.Set("X-Stainless-Package-Version", "0.74.0")
req.Header.Set("X-Stainless-Runtime", "bun")
req.Header.Set("X-Stainless-Runtime-Version", "1.3.11")
```

---

## 5. 长期维护建议

1. **版本跟踪**: CLI 和 SDK 版本频繁更新。建议将这些版本号提取为可配置常量或环境变量，
   而不是硬编码在代码中，以便跟随 CLI 升级更新。

2. **Beta 动态传递**: 考虑将 `Anthropic-Beta` header 改为从请求中透传（如果客户端已设置），
   而不是完全覆盖。或者至少将 modelserver 硬编码的 beta 与请求中已有的 beta 合并。

3. **定期审计**: 每次 Claude Code CLI 重大版本更新时，重新审计 headers 的一致性。

---

## 附录: CLI v2.1.76 关键信息

```
二进制路径:   /root/.local/share/claude/versions/2.1.76
构建时间:     2026-03-14T00:13:33Z
运行时:       Bun v1.3.11
SDK 版本:     @anthropic-ai/sdk 0.74.0
API 版本:     2023-06-01
OAuth Client: 9d1c250a-e61b-44d9-88ed-5944d1962f5e
```
