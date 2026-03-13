# API Key: Base62 编码 + 存储后缀

## 概述

将 API key 的编码从 base64url 改为 base62，将数据库中存储的可见标识从前缀 (`key_prefix`) 改为后缀 (`key_suffix`)。项目尚未上线，不需要向后兼容旧格式。

## 变更摘要

| 方面 | 当前 | 变更后 |
|------|------|--------|
| 编码 | base64url (A-Z, a-z, 0-9, `-`, `_`) | base62 (0-9, A-Z, a-z) |
| Key body 长度 | 48 字符 | 49 字符（固定，左填充 `0`） |
| Key 总长度 | 51 字符 (`ms-` + 48) | 52 字符 (`ms-` + 49) |
| 存储可见部分 | `key_prefix`：前 11 字符 (`ms-` + 8 chars) | `key_suffix`：末尾 4 字符 |
| UI 展示 | `ms-ABCDEFGH...` | `ms-...ab12` |
| 旧格式兼容 | 支持 3 种旧格式 | 仅支持 base62 格式 |

## 详细设计

### 1. Base62 编码（新文件）

**文件：** `internal/crypto/base62.go` + `internal/crypto/base62_test.go`

字符集：`0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz`

编码方式：将字节数组视为大整数（big-endian），反复除以 62 取余数映射到字符集。结果左填充字符 `0`（字符集索引 0）至固定长度。

提供两个公开函数：
- `Base62Encode(data []byte, fixedLen int) string` — 编码为固定长度 base62 字符串
- `Base62Decode(s string, expectedByteLen int) ([]byte, error)` — 解码回字节数组，左填充至指定字节长度

编码 36 字节数据的固定长度为 49 字符（`ceil(36 * 8 / log2(62)) ≈ 48.4`，取 49）。

### 2. Key 生成变更

**文件：** `internal/admin/handle_keys.go`

变更点：
- 将 `base64.RawURLEncoding.EncodeToString(combined)` 替换为 `crypto.Base62Encode(combined, 49)`
- `KeyPrefix: plaintext[:len(types.APIKeyPrefix)+8]` 替换为 `KeySuffix: plaintext[len(plaintext)-4:]`

生成流程（不变的部分）：
1. 32 字节 `crypto/rand` 随机数据
2. 4 字节 HMAC-SHA256 checksum
3. 拼接为 36 字节

变更的部分：
4. Base62 编码为 49 字符（原 base64url 48 字符）
5. 拼接前缀 `ms-` → 52 字符完整 key
6. 取末尾 4 字符作为 `key_suffix`（原取前 11 字符作为 `key_prefix`）

### 3. 数据库迁移

**新迁移文件：** `internal/store/migrations/002_apikey_suffix.sql`

```sql
ALTER TABLE api_keys DROP COLUMN key_prefix;
ALTER TABLE api_keys ADD COLUMN key_suffix TEXT NOT NULL DEFAULT '';
```

破坏性迁移，旧 key 记录的 `key_suffix` 为空字符串。

### 4. 类型变更

**文件：** `internal/types/apikey.go`

- `KeyPrefix string` → `KeySuffix string`
- JSON tag: `json:"key_prefix"` → `json:"key_suffix"`
- DB tag: `db:"key_prefix"` → `db:"key_suffix"`

### 5. Store 变更

**文件：** `internal/store/keys.go`

所有 SQL 查询中的 `key_prefix` 替换为 `key_suffix`。涉及 `CreateAPIKey`、`GetAPIKeyByHash`、`GetAPIKeyByID`、`listAPIKeys`（含 SQL 和 Scan 调用）等函数。

**文件：** `internal/store/usage.go`

Usage 查询中 `k.key_prefix` 替换为 `k.key_suffix`（SQL SELECT、GROUP BY 及结果 map key）。

### 6. 认证中间件变更

**文件：** `internal/proxy/auth_middleware.go`

- 删除所有旧格式兼容代码（hex 72/64 字符、base64url 48 字符）
- 仅支持新格式：`ms-` + 49 个 base62 字符
- 格式校验：检查长度为 52 且 body 仅含 base62 字符
- Checksum 校验：base62 解码 → 拆分 32 字节随机 + 4 字节 checksum → HMAC 校验

### 7. Crypto 包变更

**文件：** `internal/crypto/apikey.go`

- 删除 `ValidateAPIKeyChecksumHex`（hex 版）
- 修改 `ValidateAPIKeyChecksum` 的实现：将内部 base64url 解码替换为 base62 解码（函数签名不变）
- `ComputeAPIKeyChecksum` 和 `deriveSubkey` 保持不变
- 常量 `APIKeyBodyLen = 49` 替代之前隐式的 48/72/64

## 影响范围

需要修改的文件：
1. `internal/crypto/base62.go`（新建）
2. `internal/crypto/base62_test.go`（新建）
3. `internal/crypto/apikey.go`
4. `internal/crypto/apikey_test.go`
5. `internal/types/apikey.go`
6. `internal/admin/handle_keys.go`
7. `internal/store/keys.go`
8. `internal/store/usage.go`
9. `internal/store/migrations/002_apikey_suffix.sql`（新建）
10. `internal/proxy/auth_middleware.go`
11. `dashboard/src/api/types.ts`（`key_prefix` → `key_suffix`，涉及 `APIKey`、`APIKeyCreateResponse`、`UsageByKey` 接口）
12. `dashboard/src/pages/keys/KeysPage.tsx`（列头 "Prefix" → "Suffix"，字段 `key_prefix` → `key_suffix`）
13. `dashboard/src/pages/usage/UsagePage.tsx`（列头 "Prefix" → "Suffix"，字段 `key_prefix` → `key_suffix`）

## 安全考虑

- 随机数据量不变（32 字节 = 256 bit），安全性不受影响
- HMAC checksum 机制不变
- base62 字符集更小（62 vs 64），但固定长度编码补偿了信息密度差异
- SHA256 哈希存储机制不变
