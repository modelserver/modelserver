# Design: Swagger/OpenAPI Documentation for ModelServer

**Date:** 2026-03-12
**Status:** Draft

## Problem

The ModelServer project has 80+ API endpoints across three services (Admin API, Proxy API, Payment Server) with no machine-readable API documentation. Developers working with the Admin dashboard, external users consuming the Proxy API, and internal teams integrating with the Payment Server all lack a browsable, testable reference.

## Solution

Add OpenAPI 2.0 (Swagger) documentation using **swaggo/swag**, with Swagger UI hosted on the Admin server. The Admin API and Proxy API are annotated directly in the main module. The Payment Server, being a separate Go module, is documented via manually written swagger comment blocks in a dedicated file within the main module.

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Tool | swaggo/swag v1.16+ | Most popular Go swagger generator, good chi integration, annotation-based. v1.16+ required for Go generics support. |
| Spec count | Single unified spec | Simpler to maintain, Tags separate concerns clearly |
| Spec format | OpenAPI 2.0 (Swagger) | swaggo/swag generates this natively |
| UI hosting | Admin server at `/swagger/*` | Admin server is the management interface; proxy should stay lean |
| Target APIs | Admin + Proxy (annotated), Payment (manual swagger blocks) | Full coverage for all consumers |
| Payment Server | Manual swagger comment blocks in `internal/admin/swagger_payserver.go` | Payserver is a separate Go module — `swag init` cannot cross module boundaries |

## Architecture

### New Dependencies (main module go.mod)

```
github.com/swaggo/swag          # CLI tool + annotation parser (v1.16+, generics support)
github.com/swaggo/http-swagger/v2  # chi-compatible Swagger UI middleware
```

### General API Info

Add project-level annotations above `main()` in `cmd/modelserver/main.go`:

```go
// @title ModelServer API
// @version 1.0
// @description API for managing LLM proxy, projects, billing, and administration.
//
// @BasePath /
//
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description JWT Bearer token for Admin API (format: "Bearer {token}")
//
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-API-Key
// @description API Key for Proxy API. Note: the actual proxy uses Authorization header with "Bearer {api_key}" format. This separate definition avoids Swagger UI header collision with BearerAuth.
//
// @tag.name Auth
// @tag.description Authentication endpoints (login, register, OAuth, token refresh)
// @tag.name System
// @tag.description System initialization and health checks
// @tag.name Users
// @tag.description User management (superadmin only)
// @tag.name Plans
// @tag.description Subscription plan management (superadmin only)
// @tag.name Projects
// @tag.description Project CRUD operations
// @tag.name Members
// @tag.description Project member management
// @tag.name Keys
// @tag.description API key management
// @tag.name Policies
// @tag.description Rate limit policy management
// @tag.name Subscriptions
// @tag.description Subscription management
// @tag.name Orders
// @tag.description Order and billing management
// @tag.name Usage
// @tag.description Usage statistics and analytics
// @tag.name Traces
// @tag.description Request tracing and debugging
// @tag.name Channels
// @tag.description Channel management (superadmin only)
// @tag.name Routes
// @tag.description Channel route management (superadmin only)
// @tag.name Proxy
// @tag.description LLM proxy endpoints (API key auth)
// @tag.name Payment
// @tag.description Payment processing (internal service, separate microservice)
// @tag.name Billing
// @tag.description Billing webhook (HMAC auth)
```

Note: `@host` is intentionally omitted so Swagger UI uses relative URLs, working correctly in any deployment environment.

### Handler Annotations

**Closure-style handlers (Admin API):** Annotations go above the outer function.

```go
// handleListProjects lists projects for the current user.
// @Summary List projects
// @Description Returns paginated list of projects. Superadmins see all projects; regular users see only their own.
// @Tags Projects
// @Accept json
// @Produce json
// @Param page query int false "Page number" default(1)
// @Param per_page query int false "Items per page" default(20)
// @Param sort query string false "Sort field" default(created_at)
// @Param order query string false "Sort order (asc/desc)" default(desc)
// @Success 200 {object} types.ListResponse[types.Project]
// @Failure 401 {object} types.ErrorResponse
// @Failure 500 {object} types.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/projects [get]
func handleListProjects(st *store.Store) http.HandlerFunc {
```

**Method-style handlers (Proxy API):** Annotations go above the method.

```go
// HandleMessages proxies a messages request to the upstream LLM provider.
// @Summary Proxy messages
// @Description Proxies an Anthropic-compatible /v1/messages request to an upstream channel. Supports streaming.
// @Tags Proxy
// @Accept json
// @Produce json
// @Param body body object true "Anthropic messages request body (pass-through)"
// @Success 200 {object} object "Anthropic messages response (pass-through from upstream)"
// @Failure 401 {string} string "Unauthorized"
// @Failure 429 {string} string "Rate limited"
// @Security ApiKeyAuth
// @Router /v1/messages [post]
func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
```

**OAuth redirect endpoints (GET, return 302):** Annotated as redirects, not JSON responses.

```go
// @Summary Redirect to OAuth provider
// @Description Redirects the user's browser to the OAuth provider's authorization page.
// @Tags Auth
// @Param redirect_uri query string true "Client redirect URI"
// @Success 302 {string} string "Redirect to provider"
// @Router /api/v1/auth/oauth/github/redirect [get]
```

**Project-scoped endpoints:** All endpoints nested under `/api/v1/projects/{projectID}/...` (~40 endpoints) must include a `@Param projectID` path parameter:

```go
// @Param projectID path string true "Project ID (UUID)"
```

This applies to: Members, Keys, Policies, Subscriptions, Orders, Usage, Traces, and Threads endpoints.

**Health check endpoints:** The 4 health endpoints (`healthz` x2, `readyz` x2) are inline anonymous functions in `main.go`. These will be extracted to named functions (e.g., `handleHealthz`, `handleReadyz`) in `main.go` so swag can parse their annotations.

### Request Body Types

Handler-internal anonymous structs and `map[string]interface{}` patterns need named types for swag to reference. These are defined at the top of each handler file.

**`internal/admin/handle_auth.go`:**
- `OAuthCallbackRequest` — `code` string
- `RefreshRequest` — `refresh_token` string
- `TokenResponse` — `access_token` string, `refresh_token` string, `user` types.User
- `UpdateUserRequest` — `nickname` string, `status` string, `is_superadmin` bool, `max_projects` int (currently decoded into `map[string]interface{}`)

**`internal/admin/handle_projects.go`:**
- `CreateProjectRequest` — `name` string, `description` string
- `UpdateProjectRequest` — `name` string, `description` string, `status` string, `settings` json.RawMessage, `billing_tags` []string (currently decoded into `map[string]interface{}`)
- `AddMemberRequest` — `user_id` string, `role` string
- `UpdateMemberRequest` — `role` string

**`internal/admin/handle_plans.go`:**
- `CreatePlanRequest` — `name` string, `slug` string, `display_name` string, `description` string, `tier_level` int, `group_tag` string, `price_per_period` int64, `period_months` int, `credit_rules` []types.CreditRule, `model_credit_rates` map[string]types.CreditRate, `classic_rules` []types.ClassicRule
- `UpdatePlanRequest` — `name` string, `slug` string, `display_name` string, `description` string, `tier_level` int, `group_tag` string, `price_per_period` int64, `period_months` int, `is_active` bool, `credit_rules` []types.CreditRule, `model_credit_rates` map[string]types.CreditRate, `classic_rules` []types.ClassicRule (currently decoded into `map[string]interface{}`)

**`internal/admin/handle_keys.go`:**
- `CreateKeyRequest` — `name` string, `description` string, `allowed_models` []string, `rate_limit_policy_id` string
- `UpdateKeyRequest` — `name` string, `description` string, `status` string, `rate_limit_policy_id` string, `allowed_models` []string (currently decoded into `map[string]interface{}`)

**`internal/admin/handle_policies.go`:**
- `CreatePolicyRequest` — `name` string, `is_default` bool, `credit_rules` []types.CreditRule, `model_credit_rates` map[string]types.CreditRate, `classic_rules` []types.ClassicRule, `starts_at` string, `expires_at` string
- `UpdatePolicyRequest` — `name` string, `is_default` bool, `starts_at` string, `expires_at` string, `credit_rules` []types.CreditRule, `model_credit_rates` map[string]types.CreditRate, `classic_rules` []types.ClassicRule (currently decoded into `map[string]interface{}`)

**`internal/admin/handle_subscriptions.go`:**
- `CreateSubscriptionRequest` — `plan_name` string, `starts_at` string, `expires_at` string
- `UpdateSubscriptionRequest` — `status` string

**`internal/admin/handle_orders.go`:**
- `CreateOrderRequest` — `plan_slug` string, `periods` int, `channel` string

**`internal/admin/handle_channels.go`:**
- `CreateChannelRequest` — `provider` string, `name` string, `base_url` string, `api_key` string, `supported_models` []string, `weight` int, `selection_priority` int, `max_concurrent` int, `test_model` string
- `UpdateChannelRequest` — `name` string, `base_url` string, `provider` string, `api_key` string, `supported_models` []string, `weight` int, `selection_priority` int, `status` string, `max_concurrent` int, `test_model` string (currently decoded into `map[string]interface{}`)
- `CreateRouteRequest` — `project_id` string, `model_pattern` string, `channel_ids` []string, `match_priority` int, `enabled` *bool
- `UpdateRouteRequest` — `model_pattern` string, `channel_ids` []string, `match_priority` int, `enabled` bool (currently decoded into `map[string]interface{}`)

**Handlers that use `map[string]interface{}`** (7 total — will need named request types created):
- `handleUpdateUser`, `handleUpdateProject`, `handleUpdatePlan`, `handleUpdateKey`, `handleUpdatePolicy`, `handleUpdateChannel`, `handleUpdateRoute`

### Payment Server Documentation

The Payment Server is a **separate Go module** at `services/payserver/`. Since `swag init` cannot cross module boundaries, payment server endpoints are documented via a dedicated file with only swagger comment blocks:

**`internal/admin/swagger_payserver.go`** (swagger-only, no real handler code):
```go
package admin

// These are swagger documentation stubs for the Payment Server endpoints.
// The actual handlers live in services/payserver/internal/server/.

// swaggerCreatePayment documents the payment creation endpoint.
// @Summary Create payment
// @Description Creates a payment for an order via the configured payment gateway.
// @Tags Payment
// @Accept json
// @Produce json
// @Param body body PaymentAPIRequest true "Payment request"
// @Success 200 {object} PaymentAPIResponse
// @Failure 400 {object} object "Bad request"
// @Failure 409 {object} object "Order already paid"
// @Security BearerAuth
// @Router /payments [post]
func swaggerCreatePayment() {}

// ... similar stubs for /notify/wechat and /notify/alipay
```

Request/response types for payment server are duplicated as swagger-only structs in this file:
- `PaymentAPIRequest` — `order_id` string, `product_name` string, `channel` string, `currency` string, `amount` int64, `notify_url` string, `return_url` string, `metadata` map[string]string
- `PaymentAPIResponse` — `payment_ref` string, `payment_url` string, `status` string

### Proxy Response Types

The proxy `HandleMessages` and `HandleCountTokens` handlers pass through upstream responses via reverse proxy. There are no Go types for these responses. These endpoints are documented with `{object} object` and a description noting the response is an Anthropic-compatible pass-through. This is acceptable since the response schema is defined by the upstream provider, not by this project.

### Swagger UI Mount

In `cmd/modelserver/main.go`, add to the admin server:

```go
import httpSwagger "github.com/swaggo/http-swagger/v2"
import _ "github.com/modelserver/modelserver/docs/swagger" // generated swagger docs

adminRouter.Get("/swagger/*", httpSwagger.Handler(
    httpSwagger.URL("/swagger/doc.json"),
))
```

### Generated Files

Running `swag init` produces output in `docs/swagger/` (separate from hand-written docs):

```
docs/swagger/
├── docs.go       # Go package that registers the spec
├── swagger.json   # OpenAPI 2.0 JSON spec
└── swagger.yaml   # OpenAPI 2.0 YAML spec
```

These files are committed to the repository so that builds don't require the swag CLI.

### Build Integration

Create a new `Makefile` in the project root:

```makefile
.PHONY: swagger
swagger:
	swag init -g cmd/modelserver/main.go -d ./ -o docs/swagger --parseInternal --parseDependency
```

Note: The `-d ./` flag ensures swag scans the entire project directory tree. Combined with `--parseInternal`, this discovers annotations in `internal/admin/` and `internal/proxy/` packages.

Add `make swagger` to CI (`.github/workflows/`) to catch annotation parse errors on every push.

## Endpoint Coverage Summary

| Service | Tag | Count | Endpoints |
|---------|-----|-------|-----------|
| Admin | Auth | 8 | auth config, refresh, oauth callback x3, oauth redirect x3 |
| Admin | Users | 4 | me, list, get, update |
| Admin | Plans | 5 | list, create, get, update, delete |
| Admin | Projects | 5 | list, create, get, update, delete |
| Admin | Members | 4 | list, add, update, remove |
| Admin | Keys | 4 | list, create, get, update |
| Admin | Policies | 5 | list, create, get, update, delete |
| Admin | Subscriptions | 5 | list, create, get, update, usage (note: `handleSubscriptionUsage` lives in `handle_orders.go`) |
| Admin | Orders | 5 | list, create, get, cancel, available-plans |
| Admin | Usage | 2 | usage stats, list requests |
| Admin | Traces | 5 | list traces, get trace, trace requests, list threads, get thread |
| Admin | Channels | 7 | list, create, get, update, delete, stats, test |
| Admin | Routes | 4 | list, create, update, delete |
| Admin | Billing | 1 | delivery webhook |
| Proxy | Proxy | 4 | messages, count_tokens, models, usage |
| Payment | Payment | 4 | create payment, wechat notify, alipay notify, healthz |
| System | Health | 4 | healthz x2, readyz x2 (main module proxy + admin servers) |
| **Total** | | **~76** | |

Note: `GET /me` is listed under Users. `GET /available-plans` is listed under Orders. OAuth redirect endpoints return 302 redirects (not JSON).

## Non-Goals

- **OpenAPI 3.0 migration** — swaggo/swag generates OpenAPI 2.0; this is sufficient for Swagger UI.
- **API client generation** — Out of scope. The spec can be used for this later.
- **Automated testing from spec** — Out of scope for this iteration.
- **Replacing `map[string]interface{}` handler logic** — Named request types are created alongside existing map-based logic for annotation purposes. The actual decoding logic is not refactored (types are only used in swag annotations, not in handler bodies).

## Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| Annotation drift | Medium | Add `make swagger` to CI to catch parse errors |
| Anonymous struct + map extraction | Medium | ~11 handler files need named types. Low risk: only adding types, not changing logic. 7 of these use `map[string]interface{}` where the named type is annotation-only. |
| Proxy responses undocumented | Low | Pass-through responses documented as generic objects with description. Acceptable since schema is defined by upstream provider. |
| Payment Server annotations manual | Low | Only 3 endpoints. Comment blocks in `swagger_payserver.go` are easy to maintain. |
| swag version compatibility | Low | Require swag v1.16+ for generics support. Pin in Makefile or document in README. |
| Health endpoint extraction | Low | 4 inline anonymous handlers in main.go need to be extracted to named functions. Trivial refactor. |
