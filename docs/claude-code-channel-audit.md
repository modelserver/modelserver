# Claude Code Channel 实现审计报告

> 审计日期: 2026-03-15
> 最后更新: 2026-03-15
> Claude Code CLI 版本: 2.1.76 (binary at `/root/.local/share/claude/versions/2.1.76`)
> Modelserver 分支: main

## 概述

本报告对比 Claude Code CLI v2.1.76 的实际行为与 modelserver 中 Claude Code channel 的代理实现，
覆盖 OAuth 流程、API 请求 headers、token 刷新逻辑、Test Connection 以及 Beta 透传等方面。

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

## 2. HTTP Headers 对比（LLM API 代理请求）

> 代码路径: `internal/proxy/claudecode.go` — `directorSetClaudeCodeUpstream()`

### 2.1 核心 Anthropic Headers — ✅ 全部一致

| Header | CLI 实际值 | Modelserver 值 | 状态 |
|--------|-----------|---------------|------|
| `Authorization` | `Bearer {token}` | `Bearer {token}` | ✅ |
| `Anthropic-Version` | `2023-06-01` | `2023-06-01` | ✅ |
| `Anthropic-Dangerous-Direct-Browser-Access` | `true` | `true` | ✅ |
| `X-App` | `cli` | `cli` | ✅ |

### 2.2 Anthropic-Beta Header — ✅ 已修复 + 动态合并

**基础集合 (硬编码，与 CLI JHA 一致):**
```
claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27
```

**动态合并机制:** Modelserver 现在从客户端请求中读取 `Anthropic-Beta` header，
将客户端发送的额外 beta flags（如 `context-1m-2025-08-07`）与基础集合合并后发送到上游，
确保基础 beta 始终存在，同时透传客户端新增的 beta。

实现: `mergeClaudeCodeBetas()` in `internal/proxy/claudecode.go`

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

### 2.3 User-Agent — ✅ 已修复

| 项目 | CLI 实际值 | Modelserver 值 | 状态 |
|------|-----------|---------------|------|
| User-Agent | `claude-cli/2.1.76 (external, cli)` | `claude-cli/2.1.76 (external, cli)` | ✅ |

CLI 的 User-Agent 格式为:
```
claude-cli/{VERSION} (external, {ENTRYPOINT}{agent_sdk}{client_app}{workload})
```
其中 VERSION 来自编译时常量，ENTRYPOINT 通常为 `cli`。

### 2.4 X-Stainless SDK Headers — ✅ 已修复

| Header | CLI 实际值 (v2.1.76) | Modelserver 值 | 状态 |
|--------|---------------------|---------------|------|
| `X-Stainless-Lang` | `js` | `js` | ✅ |
| `X-Stainless-Package-Version` | `0.74.0` | `0.74.0` | ✅ |
| `X-Stainless-OS` | `Linux` | `Linux` | ✅ |
| `X-Stainless-Runtime` | `bun` | `bun` | ✅ |
| `X-Stainless-Runtime-Version` | `1.3.11` | `1.3.11` | ✅ |
| `X-Stainless-Arch` | `x64` | `x64` | ✅ |

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

## 3. Test Connection Headers — ✅ 已修复

> 代码路径: `internal/admin/handle_channels.go` — `handleTestChannel()`

Test Connection 对 Claude Code channel 的请求现在与代理请求保持一致的 headers:

| Header | 值 | 状态 |
|--------|---|------|
| `Authorization` | `Bearer {access_token}` | ✅ |
| `Anthropic-Version` | `2023-06-01` | ✅ |
| `Anthropic-Beta` | `claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27` | ✅ |
| `Anthropic-Dangerous-Direct-Browser-Access` | `true` | ✅ |
| `X-App` | `cli` | ✅ |
| `User-Agent` | `claude-cli/2.1.76 (external, cli)` | ✅ |
| `X-Stainless-Lang` | `js` | ✅ |
| `X-Stainless-Package-Version` | `0.74.0` | ✅ |
| `X-Stainless-OS` | `Linux` | ✅ |
| `X-Stainless-Runtime` | `bun` | ✅ |
| `X-Stainless-Runtime-Version` | `1.3.11` | ✅ |
| `X-Stainless-Arch` | `x64` | ✅ |
| `?beta=true` query param | 包含在 endpoint URL 中 | ✅ |

**注意:** Test Connection 是管理员发起的一次性检测请求，不经过 reverse proxy director，
因此 headers 需要独立维护。未来更新 headers 时需同步修改两处。

---

## 4. 已修复问题追溯

### 初始审计发现的问题（已全部修复）:

| # | 问题 | 原始优先级 | 修复 commit | 状态 |
|---|------|----------|------------|------|
| 1 | `Anthropic-Beta` 包含不存在的 `fine-grained-tool-streaming` | 🔴 高 | d10ed79 | ✅ 已修复 |
| 2 | `Anthropic-Beta` 缺失核心 `context-management` | 🔴 高 | d10ed79 | ✅ 已修复 |
| 3 | `User-Agent` 版本过时 (`1.0.83` → `2.1.76`) | 🔴 高 | d10ed79 | ✅ 已修复 |
| 4 | `X-Stainless-Package-Version` 过时 (`0.52.0` → `0.74.0`) | 🟡 中 | d10ed79 | ✅ 已修复 |
| 5 | `X-Stainless-Runtime` 错误 (`node` → `bun`) | 🟡 中 | d10ed79 | ✅ 已修复 |
| 6 | `X-Stainless-Runtime-Version` 错误 | 🟡 中 | d10ed79 | ✅ 已修复 |
| 7 | `Anthropic-Beta` 中多余的 `oauth-2025-04-20` | 🟡 中 | d10ed79 | ✅ 已修复 |
| 8 | `Connection: keep-alive` 多余 | 🟢 低 | - | 保留（无害） |

### 本次审阅新发现并修复的问题:

| # | 问题 | 优先级 | 状态 |
|---|------|-------|------|
| 9 | Test Connection headers 未同步修复 — beta/UA/Stainless 全部过时 | 🔴 高 | ✅ 已修复 |
| 10 | Beta header 硬覆盖导致客户端新增 beta 被丢弃 | 🔴 高 | ✅ 已修复（合并策略） |

---

## 5. 剩余低优先级项

### 🟢 `Connection: keep-alive` 多余（保留）

HTTP/1.1 默认行为。无害但多余，保留不影响功能。

---

## 6. 长期维护建议

1. **版本跟踪**: CLI 和 SDK 版本频繁更新。建议将这些版本号提取为可配置常量或环境变量，
   而不是硬编码在代码中，以便跟随 CLI 升级更新。

2. **Beta 动态合并 (已实现)**: `mergeClaudeCodeBetas()` 函数确保基础 beta 始终存在，
   同时透传客户端发送的额外 beta flags。当 CLI 添加新 beta（如 `context-1m`）时，
   modelserver 无需代码修改即可透传。

3. **Test Connection 同步**: `handleTestChannel()` 中的 Claude Code headers 需要
   与 `directorSetClaudeCodeUpstream()` 手动保持同步。未来可考虑提取为共享的 header 设置函数。

4. **定期审计**: 每次 Claude Code CLI 重大版本更新时，重新审计 headers 的一致性。

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
