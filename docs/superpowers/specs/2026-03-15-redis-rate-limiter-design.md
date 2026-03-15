# Redis Rate Limiter & Session Binding Design

## Goal

Migrate the rate limiter and channel router session binding from in-memory storage to Redis, enabling horizontal scaling of modelserver instances. When Redis is not configured, fall back to the existing in-memory implementation.

## Background

The current rate limiter uses two in-memory data structures that prevent horizontal scaling:

1. **Classic rate limiting** (`MemoryCounters`): RPM/TPM/RPD/TPD counters stored as timestamp slices and token entries behind a `sync.RWMutex`. Counters reset on restart.
2. **Credit-based rate limiting** (`creditCache`): 10-second TTL cache of `SumCreditsInWindow` DB query results. Each instance maintains its own cache, so multiple instances see different credit totals.

Additionally, the `ChannelRouter` maintains an in-memory `sessions` map (trace ID → channel ID) to ensure a trace routes to the same channel. Multiple instances cannot share this binding.

## Approach: Redis Atomic Counters

All rate limiting state moves to Redis using atomic increment operations. Credit totals are maintained incrementally in Redis (via `INCRBYFLOAT`) rather than queried from the database. Classic counters use `INCRBY`. Session bindings use simple `SET`/`GET` with TTL.

This eliminates DB queries from the hot path of rate limit checks.

## Architecture

### Interface Abstraction

Two new interfaces decouple storage from logic:

```go
// ratelimit.Backend — used by CompositeRateLimiter
type Backend interface {
    IncrCredit(ctx context.Context, key string, amount float64, ttl time.Duration) (float64, error)
    GetCreditSum(ctx context.Context, keys []string) (float64, error)
    IncrRequestCount(ctx context.Context, key string, ttl time.Duration) (int64, error)
    GetRequestCount(ctx context.Context, key string) (int64, error)
    IncrTokenCount(ctx context.Context, key string, amount int64, ttl time.Duration) (int64, error)
    GetTokenCount(ctx context.Context, key string) (int64, error)
}

// proxy.SessionStore — used by ChannelRouter
type SessionStore interface {
    Get(ctx context.Context, traceID string) (channelID string, ok bool)
    Set(ctx context.Context, traceID string, channelID string, ttl time.Duration) error
    Delete(ctx context.Context, traceID string) error
}
```

All increment methods return the new total, enabling future combine-and-check optimizations.

Each interface has two implementations: `MemoryBackend`/`MemorySessionStore` (existing logic, repackaged) and `RedisBackend`/`RedisSessionStore` (new).

### RateLimiter Interface Change

The existing `RateLimiter.PostRecord` receives `types.TokenUsage` (raw token counts), but the `Backend` needs computed credits. The handler already computes credits via `policy.ComputeCredits()` before calling `PostRecord`. The `PostRecord` signature changes to include the pre-computed credits:

```go
type RateLimiter interface {
    PreCheck(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy) (bool, time.Duration, error)
    PostRecord(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy, usage types.TokenUsage, credits float64)
}
```

The handler passes the already-computed `credits` value. `CompositeRateLimiter.PostRecord` uses `credits` for `Backend.IncrCredit` and `usage` for classic token counters.

### Initialization

At startup, `main.go` checks for Redis configuration:

```go
if cfg.Redis.URL != "" {
    rdb := redis.NewClient(...)
    rlBackend = ratelimit.NewRedisBackend(rdb, cfg.Redis.FailOpen, cfg.Redis.KeyPrefix)
    sessionStore = proxy.NewRedisSessionStore(rdb, cfg.Redis.FailOpen, cfg.Redis.KeyPrefix)
} else {
    logger.Warn("Redis not configured, using in-memory rate limiter (not suitable for horizontal scaling)")
    rlBackend = ratelimit.NewMemoryBackend()
    sessionStore = proxy.NewMemorySessionStore()
}
```

## Redis Key Design

### Credit Rate Limiting

**Key format**: `{prefix}rl:credit:{scope}:{scopeID}:{windowTag}:{bucketTimestamp}`

- `scope`: `p` (project) or `k` (key)
- `scopeID`: project ID or API key ID
- `windowTag`: window identifier (e.g., `5h`, `7d`, `1M`)
- `bucketTimestamp`: bucket start time as Unix seconds, aligned to bucket boundary

**Example**:
```
rl:credit:p:proj_123:5h:1710489600  → "152.75"
rl:credit:p:proj_123:5h:1710489900  → "88.30"
```

**Operations**:
- `PostRecord`: `INCRBYFLOAT key credits` + `EXPIRE key (windowDuration + bucketSize)`
- `PreCheck`: `MGET key1 key2 ... keyN` → sum all values → compare with `MaxCredits`

### Classic Rate Limiting

**Key format**: `{prefix}rl:classic:{apiKeyID}:{metric}[:{model}]:{bucketTimestamp}`

Classic rate limiting also uses the dynamic `bucketSize()` function:

- RPM/TPM (1-minute window): 1-second buckets (60 keys per MGET)
- RPD/TPD (24-hour window): 30-minute buckets (48 keys per MGET)

**Operations**: Same pattern as credit — `INCRBY`/`INCRBYFLOAT` on PostRecord, `MGET` + sum on PreCheck.

### Session Binding

**Key format**: `{prefix}session:{traceID}`

The trace ID is used as the session identifier (the existing code calls it `sessionID` in `SelectChannelForSession`, but the value is always the trace ID).

**Operations**:
- `Set`: `SET session:{traceID} {channelID} EX {ttl_seconds}`
- `Get`: `GET session:{traceID}`
- `Delete`: `DEL session:{traceID}`

### Calendar Windows

Calendar windows (`1M`, `1w`) are single-key since the window itself is a fixed boundary:

- `1M`: key aligned to month start (1st day 00:00 UTC), TTL = until end of next month
- `1w`: key aligned to week start (Monday 00:00 UTC), TTL = until end of next week

## Sliding Window Bucket Granularity

Bucket size is computed dynamically based on window duration, targeting 60-120 buckets per window:

| Window Duration | Bucket Size | Approx. Buckets |
|----------------|-------------|-----------------|
| ≤1h            | 1min        | ≤60             |
| 1h–12h         | 5min        | 12–144          |
| 12h–3d         | 30min       | 24–144          |
| 3d–30d         | 1h          | 72–720          |
| >30d           | 4h          | ≤180            |

A function `bucketSize(windowDuration) time.Duration` implements this lookup. This supports arbitrary window sizes (any `Xh`, `Yd`, etc.) without hardcoding.

## Configuration

New `redis` section in `config.yml`:

```yaml
redis:
  url: ""              # Redis connection URL. Empty = fall back to in-memory.
  password: ""         # Optional auth password.
  db: 0                # Redis database number.
  pool_size: 20        # Connection pool size. Default: 20.
  key_prefix: ""       # Optional key prefix for namespace isolation (e.g., "prod:" or "staging:").
  fail_open: true      # Behavior when Redis is unavailable. true = allow requests; false = reject. Default: true.
```

Environment variable mapping:
- `MODELSERVER_REDIS_URL`
- `MODELSERVER_REDIS_PASSWORD`
- `MODELSERVER_REDIS_DB`
- `MODELSERVER_REDIS_POOL_SIZE`
- `MODELSERVER_REDIS_KEY_PREFIX`
- `MODELSERVER_REDIS_FAIL_OPEN`

## Fault Tolerance

### Redis Unavailable at Runtime

Controlled by `fail_open` configuration:

- `fail_open: true` (default): `RedisBackend` and `RedisSessionStore` methods return zero/empty values on error (effectively skipping rate limit check and selecting a new channel). Consistent with current behavior where DB query failures allow requests through.
- `fail_open: false`: `RedisBackend` and `RedisSessionStore` methods propagate errors, causing the middleware to return HTTP 503.

The `fail_open` setting applies to both rate limiting and session binding uniformly. For session binding specifically, fail-open means `Get` returns `("", false)` — the router selects a new channel as if no session existed.

### Redis Restart / Data Loss

Credit counters reset to zero. During the recovery window, rate limits are temporarily relaxed until new requests rebuild the counters. This is an accepted tradeoff for the performance benefit of not querying the database.

### No Redis Configured

Falls back to `MemoryBackend` and `MemorySessionStore`. Behavior is identical to current implementation. A warning is logged at startup.

## Non-Hot-Path DB Queries

The following endpoints continue to query credits from the database (not Redis), since they are not on the proxy hot path and need historical accuracy:

- `/v1/usage` — reports usage/credit progress to the user via `SumCreditsInWindow` / `SumCreditsInWindowByProject`
- Admin order creation — checks credit sums via `SumCreditsInWindowByProject`

These DB-based queries may show slightly different totals than Redis counters (which are eventually consistent). This is acceptable: the usage API reports historical truth from the `requests` table, while Redis enforces real-time rate limits.

## Health Check

When Redis is configured, the `/readyz` endpoint additionally runs `PING` against Redis. If Redis is unreachable, `/readyz` returns unhealthy (consistent with the existing DB connectivity check).

## File Changes

### New Files

| File | Purpose |
|------|---------|
| `internal/ratelimit/backend.go` | `Backend` interface definition |
| `internal/ratelimit/redis_backend.go` | Redis implementation of `Backend` |
| `internal/ratelimit/memory_backend.go` | In-memory implementation of `Backend` using bucket semantics (same conceptual model as Redis, implemented with `map[string]float64`/`map[string]int64` + mutex) |
| `internal/ratelimit/bucket.go` | Bucket size calculation, window-to-keys generation |
| `internal/proxy/session_store.go` | `SessionStore` interface definition |
| `internal/proxy/redis_session_store.go` | Redis implementation of `SessionStore` |
| `internal/proxy/memory_session_store.go` | In-memory implementation of `SessionStore` |
| `internal/config/redis.go` | `RedisConfig` struct |

### Modified Files

| File | Changes |
|------|---------|
| `internal/ratelimit/composite.go` | Replace `classic`/`cache` fields with `Backend` interface; rewrite PreCheck/PostRecord to use Backend methods |
| `internal/proxy/channel_router.go` | Replace `sessions` map with `SessionStore` interface; remove `cleanupExpired()` goroutine |
| `internal/config/config.go` | Add `Redis RedisConfig` field |
| `config.example.yml` | Add `redis` config section |
| `cmd/modelserver/main.go` | Add Redis client init, Backend/SessionStore selection, graceful shutdown (`rdb.Close()` in shutdown handler), Redis health check |
| `internal/proxy/handler.go` | Pass `credits` to `PostRecord` call (signature change) |
| `internal/proxy/openai_handler.go` | Pass `credits` to `PostRecord` call (signature change) |
| `internal/proxy/router.go` | Pass `SessionStore` to `ChannelRouter` constructor |
| `go.mod` | Add `github.com/redis/go-redis/v9` dependency |

### Deletable Files

| File | Reason |
|------|--------|
| `internal/ratelimit/classic.go` | Replaced by bucket-based counters in `memory_backend.go` |
| `internal/ratelimit/memory.go` | Replaced by bucket-based counters in `memory_backend.go` |
| `internal/ratelimit/cache.go` | Replaced by bucket-based counters in `memory_backend.go` |

## Testing

- **Unit tests**: Both `Backend` implementations tested for credit increment/query, classic counting, and bucket expiry behavior.
- **Integration tests**: `RedisBackend` tested with `miniredis` (pure Go in-memory Redis server), no real Redis required.
- **Regression**: `CompositeRateLimiter` tests run through `MemoryBackend` to verify existing behavior is preserved.
