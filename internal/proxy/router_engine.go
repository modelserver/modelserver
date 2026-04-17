package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/proxy/lb"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// sessionBinding tracks which upstream a (session, model) pair is pinned to.
type sessionBinding struct {
	upstreamID string
	usedAt     time.Time
}

// sessionKey buckets affinity by (sessionID, model): same pair pins to the
// same upstream. Using a struct (rather than a concatenated string) is
// type-safe and keeps the two-axis intent visible.
type sessionKey struct {
	sessionID string
	model     string
}

// Router is the central routing engine (nginx: the config evaluator).
// It is the central routing engine for the upstream-based routing model.
type Router struct {
	mu            sync.RWMutex
	upstreams     map[string]*types.Upstream    // id -> upstream
	groups        map[string]*resolvedGroup     // groupID -> resolved group
	routes        []types.Route                 // sorted by match_priority descending
	decryptedKeys map[string]string             // upstreamID -> API key

	balancers      map[string]*lb.Balancer      // groupID -> balancer
	circuitBreaker *lb.CircuitBreaker
	healthChecker  *lb.HealthChecker
	connTracker    *lb.ConnectionTracker
	metrics        *lb.UpstreamMetrics

	sessionMap sync.Map // sessionKey -> sessionBinding
	sessionTTL time.Duration

	vertexTokenManager *VertexTokenManager

	catalog modelcatalog.Catalog // for ActiveModels and status checks

	logger   *slog.Logger
	oauthMgr *OAuthTokenManager
}

type resolvedGroup struct {
	group   types.UpstreamGroup
	members []groupMember
}

type groupMember struct {
	upstream *types.Upstream
	weight   int
	isBackup bool
}

// SelectedUpstream is returned by SelectWithRetry and contains the upstream
// along with its decrypted API key, ready for the executor to use.
type SelectedUpstream struct {
	Upstream *types.Upstream
	APIKey   string
}

// NewRouter creates a new routing engine from the given configuration.
// The catalog is used by ActiveModels for status filtering; callers can pass
// nil during tests that don't exercise that surface.
func NewRouter(
	upstreams []types.Upstream,
	groups []store.UpstreamGroupWithMembers,
	routes []types.Route,
	encKey []byte,
	logger *slog.Logger,
	sessionTTL time.Duration,
	oauthMgr *OAuthTokenManager,
	catalog modelcatalog.Catalog,
) *Router {
	r := &Router{
		sessionTTL: sessionTTL,
		logger:     logger,
		oauthMgr:   oauthMgr,
		catalog:    catalog,
	}

	// Create the Vertex AI token manager.
	r.vertexTokenManager = NewVertexTokenManager()

	// Wire the token manager into the already-registered VertexAnthropicTransformer.
	SetVertexAnthropicTokenManager(r.vertexTokenManager)

	// Wire the same token manager into the VertexGoogleTransformer and VertexOpenAITransformer.
	SetVertexGoogleTokenManager(r.vertexTokenManager)
	SetVertexOpenAITokenManager(r.vertexTokenManager)

	// Build shared infrastructure components.
	r.connTracker = lb.NewConnectionTracker()
	r.metrics = lb.NewUpstreamMetrics()
	r.circuitBreaker = lb.NewCircuitBreaker(5, 2, 30*time.Second)
	r.healthChecker = lb.NewHealthChecker(r.circuitBreaker, r.metrics, logger, r.vertexTokenManager.GetToken)

	// Build all maps from the configuration.
	r.buildMaps(upstreams, groups, routes, encKey)

	return r
}

// buildMaps constructs all internal maps from the raw configuration.
// Called by NewRouter and Reload. Must be called under write lock (or during construction).
func (r *Router) buildMaps(
	upstreams []types.Upstream,
	groups []store.UpstreamGroupWithMembers,
	routes []types.Route,
	encKey []byte,
) {
	// 1. Build upstreams map.
	um := make(map[string]*types.Upstream, len(upstreams))
	for i := range upstreams {
		um[upstreams[i].ID] = &upstreams[i]
	}

	// 2. Decrypt all upstream API keys.
	keys := decryptUpstreamKeys(upstreams, encKey, r.logger)

	// Register Vertex AI service account keys with the token manager.
	if r.vertexTokenManager != nil {
		r.vertexTokenManager.Clear()
		for _, u := range upstreams {
			if u.Provider == types.ProviderVertexAnthropic || u.Provider == types.ProviderVertexGoogle || u.Provider == types.ProviderVertexOpenAI {
				if key, ok := keys[u.ID]; ok {
					if err := r.vertexTokenManager.Register(u.ID, []byte(key)); err != nil {
						r.logger.Error("failed to register vertex token source",
							"upstream_id", u.ID, "error", err)
					}
				}
			}
		}
	}

	// 3. Resolve groups: for each group, find its member upstreams from the map,
	//    compute effective weight (member override or upstream default).
	gm := make(map[string]*resolvedGroup, len(groups))
	balancers := make(map[string]*lb.Balancer, len(groups))
	for _, gwm := range groups {
		rg := &resolvedGroup{
			group:   gwm.UpstreamGroup,
			members: make([]groupMember, 0, len(gwm.Members)),
		}
		for _, md := range gwm.Members {
			u, ok := um[md.UpstreamID]
			if !ok {
				if r.logger != nil {
					r.logger.Warn("upstream group member references unknown upstream",
						"group_id", gwm.ID,
						"upstream_id", md.UpstreamID)
				}
				continue
			}
			weight := u.Weight
			if md.Weight != nil {
				weight = *md.Weight
			}
			rg.members = append(rg.members, groupMember{
				upstream: u,
				weight:   weight,
				isBackup: md.IsBackup,
			})
		}
		gm[gwm.ID] = rg

		// Create a balancer for this group using its LB policy.
		policy := gwm.LBPolicy
		if policy == "" {
			policy = types.LBPolicyWeightedRandom
		}
		balancers[gwm.ID] = lb.NewBalancer(policy, r.connTracker)
	}

	// 4. Sort routes by match_priority descending (highest priority first).
	sortedRoutes := make([]types.Route, len(routes))
	copy(sortedRoutes, routes)
	sort.Slice(sortedRoutes, func(i, j int) bool {
		return sortedRoutes[i].MatchPriority > sortedRoutes[j].MatchPriority
	})

	// 5. Register all upstreams with TestModel set for health checking.
	for _, u := range upstreams {
		if u.TestModel == "" {
			continue
		}
		interval := 30 * time.Second
		timeout := 5 * time.Second
		if u.HealthCheck != nil {
			if u.HealthCheck.Interval > 0 {
				interval = u.HealthCheck.Interval
			}
			if u.HealthCheck.Timeout > 0 {
				timeout = u.HealthCheck.Timeout
			}
		}
		apiKey := keys[u.ID]
		r.healthChecker.Register(u.ID, u.Provider, u.BaseURL, u.TestModel, apiKey, interval, timeout)
	}

	// Assign to Router fields.
	r.upstreams = um
	r.groups = gm
	r.routes = sortedRoutes
	r.decryptedKeys = keys
	r.balancers = balancers

	// Load/reload OAuth credentials for claudecode upstreams.
	if r.oauthMgr != nil {
		r.oauthMgr.Reload(upstreams, keys)
	}
}

// Match finds the upstream group for a request (project + model).
// It checks project-specific routes first, then global routes.
// No auto-discovery fallback — all routing must go through explicit routes.
func (r *Router) Match(projectID, model string) (*resolvedGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. Try project-specific routes (highest match_priority first, routes are pre-sorted).
	for _, route := range r.routes {
		if route.Status != "active" {
			continue
		}
		if route.ProjectID == projectID && slices.Contains(route.ModelNames, model) {
			if g, ok := r.groups[route.UpstreamGroupID]; ok {
				return g, nil
			}
		}
	}

	// 2. Fall back to global routes (projectID = "").
	for _, route := range r.routes {
		if route.Status != "active" {
			continue
		}
		if route.ProjectID == "" && slices.Contains(route.ModelNames, model) {
			if g, ok := r.groups[route.UpstreamGroupID]; ok {
				return g, nil
			}
		}
	}

	return nil, fmt.Errorf("no route configured for model %s", model)
}

// SelectWithRetry returns an ordered list of upstreams to try for the given group.
// The first element is the primary pick; subsequent elements are retry fallbacks.
// Filtering applies: open circuits, HealthDown, MaxConcurrent at capacity, and
// draining upstreams are excluded. HealthDegraded upstreams have their effective
// weight reduced to 25% (minimum 1).
func (r *Router) SelectWithRetry(ctx context.Context, group *resolvedGroup, sessionID, model string) []*SelectedUpstream {
	r.mu.RLock()
	defer r.mu.RUnlock()

	maxRetries := 0
	if group.group.RetryPolicy != nil {
		maxRetries = group.group.RetryPolicy.MaxRetries
	}
	n := 1 + maxRetries // primary + retries

	// 1. Build candidate lists (primary and backup), filtering out ineligible upstreams.
	var primaryCandidates []lb.CandidateInfo
	var backupCandidates []lb.CandidateInfo

	for _, m := range group.members {
		uid := m.upstream.ID

		// Skip disabled upstreams.
		if m.upstream.Status == types.UpstreamStatusDisabled {
			continue
		}

		// Skip draining upstreams (no new requests).
		if m.upstream.Status == types.UpstreamStatusDraining {
			continue
		}

		// Skip upstreams with open circuit breakers.
		if !r.circuitBreaker.CanPass(uid) {
			continue
		}

		// Skip upstreams that are HealthDown.
		if r.healthChecker.Status(uid) == lb.HealthDown {
			continue
		}

		// Skip upstreams at MaxConcurrent capacity.
		if m.upstream.MaxConcurrent > 0 {
			if r.connTracker.Count(uid) >= int64(m.upstream.MaxConcurrent) {
				continue
			}
		}

		// Compute effective weight: downweight HealthDegraded (25%, minimum 1).
		effectiveWeight := m.weight
		if r.healthChecker.Status(uid) == lb.HealthDegraded {
			effectiveWeight = effectiveWeight / 4
			if effectiveWeight < 1 {
				effectiveWeight = 1
			}
		}

		ci := lb.CandidateInfo{
			Upstream:    m.upstream,
			Weight:      effectiveWeight,
			IsBackup:    m.isBackup,
			ActiveConns: r.connTracker.Count(uid),
		}

		if m.isBackup {
			backupCandidates = append(backupCandidates, ci)
		} else {
			primaryCandidates = append(primaryCandidates, ci)
		}
	}

	// 5. If no primary candidates remain, use backups.
	candidates := primaryCandidates
	if len(candidates) == 0 {
		candidates = backupCandidates
	}

	if len(candidates) == 0 {
		return nil
	}

	// 6. Resolve the group's balancer once (shared across affinity and ranking).
	balancer, ok := r.balancers[group.group.ID]
	if !ok {
		balancer = lb.NewBalancer(group.group.LBPolicy, r.connTracker)
	}

	// 7. Session affinity. Concurrent requests with the same (sessionID, model)
	//    must converge on the same primary upstream. Writing the binding only
	//    after the upstream responds (as the executor does via BindSession)
	//    leaves a multi-second window in which parallel requests all observe
	//    "no binding" and independently balance-pick — silently splitting one
	//    (session, model) across multiple upstreams. Claim the binding here,
	//    atomically, so that losers in the concurrent race read and follow
	//    the winner.
	if sessionID != "" && model != "" {
		key := sessionKey{sessionID: sessionID, model: model}
		if primary := r.pinSessionToUpstream(key, candidates, balancer); primary != nil {
			return r.resultWithPrimary(primary, candidates, balancer, n)
		}
		// pinSessionToUpstream returns nil only when the balancer can produce
		// no primary (e.g. all candidate weights collapse to zero). Fall
		// through to plain ranking below so the caller still gets a result.
	}

	// 8. No session affinity: rank via balancer directly.
	ranked := balancer.SelectN(candidates, n)
	result := make([]*SelectedUpstream, len(ranked))
	for i, u := range ranked {
		result[i] = &SelectedUpstream{
			Upstream: u,
			APIKey:   r.decryptedKeys[u.ID],
		}
	}
	return result
}

// pinSessionToUpstream resolves the (session, model) key to a primary
// upstream from candidates, establishing or refreshing the binding as a
// side effect. It handles three cases atomically against concurrent
// callers:
//
//  1. Valid binding pointing at an available upstream — use it, refresh
//     the timestamp.
//  2. No binding, or binding stale/expired/pointing at an unavailable
//     upstream — pick via balancer and install the binding via LoadOrStore.
//     If another goroutine wrote first, follow the winner (provided the
//     winner's upstream is available); otherwise our pick wins and overwrites.
//
// Returns nil only when the balancer cannot produce a primary.
func (r *Router) pinSessionToUpstream(key sessionKey, candidates []lb.CandidateInfo, balancer *lb.Balancer) *types.Upstream {
	findInCandidates := func(id string) *types.Upstream {
		for _, c := range candidates {
			if c.Upstream.ID == id {
				return c.Upstream
			}
		}
		return nil
	}

	// Fast path: existing, fresh binding to an available upstream.
	if val, ok := r.sessionMap.Load(key); ok {
		binding := val.(sessionBinding)
		if time.Since(binding.usedAt) < r.sessionTTL {
			if u := findInCandidates(binding.upstreamID); u != nil {
				r.sessionMap.Store(key, sessionBinding{
					upstreamID: u.ID,
					usedAt:     time.Now(),
				})
				return u
			}
		}
	}

	// Slow path: pick a fresh primary and try to claim the (session, model).
	primary := balancer.Select(candidates)
	if primary == nil {
		return nil
	}
	newBinding := sessionBinding{upstreamID: primary.ID, usedAt: time.Now()}

	actual, loaded := r.sessionMap.LoadOrStore(key, newBinding)
	if !loaded {
		// We won the race for this previously-unbound (session, model).
		return primary
	}

	// Someone else's binding is already present. Use it only if still usable;
	// otherwise our pick wins and we overwrite the stale/expired binding.
	existing := actual.(sessionBinding)
	if time.Since(existing.usedAt) < r.sessionTTL {
		if u := findInCandidates(existing.upstreamID); u != nil {
			return u
		}
	}
	r.sessionMap.Store(key, newBinding)
	return primary
}

// resultWithPrimary builds the ranked SelectedUpstream list with the given
// primary as element 0, and balancer-selected fallbacks from the remaining
// candidates filling retry slots 1..n-1.
func (r *Router) resultWithPrimary(primary *types.Upstream, candidates []lb.CandidateInfo, balancer *lb.Balancer, n int) []*SelectedUpstream {
	result := make([]*SelectedUpstream, 0, n)
	result = append(result, &SelectedUpstream{
		Upstream: primary,
		APIKey:   r.decryptedKeys[primary.ID],
	})
	if n <= 1 || len(candidates) <= 1 {
		return result
	}
	remaining := make([]lb.CandidateInfo, 0, len(candidates)-1)
	for _, c := range candidates {
		if c.Upstream.ID != primary.ID {
			remaining = append(remaining, c)
		}
	}
	for _, u := range balancer.SelectN(remaining, n-1) {
		result = append(result, &SelectedUpstream{
			Upstream: u,
			APIKey:   r.decryptedKeys[u.ID],
		})
	}
	return result
}

// BindSession stores a (session, model)-to-upstream binding for stickiness.
func (r *Router) BindSession(sessionID, model, upstreamID string) {
	if sessionID == "" || model == "" {
		return
	}
	r.sessionMap.Store(sessionKey{sessionID: sessionID, model: model}, sessionBinding{upstreamID: upstreamID, usedAt: time.Now()})
}

// StartSessionCleanup runs a background goroutine that periodically removes
// expired session bindings.
func (r *Router) StartSessionCleanup(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			r.sessionMap.Range(func(key, value any) bool {
				if now.Sub(value.(sessionBinding).usedAt) > r.sessionTTL {
					r.sessionMap.Delete(key)
				}
				return true
			})
		}
	}()
}

// Reload replaces all configuration atomically, rebuilding all internal maps.
func (r *Router) Reload(
	upstreams []types.Upstream,
	groups []store.UpstreamGroupWithMembers,
	routes []types.Route,
	encKey []byte,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buildMaps(upstreams, groups, routes, encKey)
}

// GetUpstreamKey returns the decrypted API key for an upstream, or empty string if not found.
func (r *Router) GetUpstreamKey(upstreamID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.decryptedKeys[upstreamID]
}

// GetClaudeCodeAccessToken returns a valid OAuth access token for a claudecode upstream,
// refreshing if necessary. Returns an error if the manager is not configured or
// the upstream has no credentials.
func (r *Router) GetClaudeCodeAccessToken(upstreamID string) (string, error) {
	if r.oauthMgr == nil {
		return "", fmt.Errorf("OAuthTokenManager not configured")
	}
	return r.oauthMgr.GetAccessToken(upstreamID)
}

// ForceRefreshClaudeCodeAccessToken unconditionally refreshes the OAuth token
// for a claudecode upstream, bypassing the normal expiry-buffer check. Used to
// recover from 401/403 responses.
func (r *Router) ForceRefreshClaudeCodeAccessToken(upstreamID string) (string, error) {
	if r.oauthMgr == nil {
		return "", fmt.Errorf("OAuthTokenManager not configured")
	}
	return r.oauthMgr.ForceRefreshAccessToken(upstreamID)
}

// ConnTracker returns the shared connection tracker.
func (r *Router) ConnTracker() *lb.ConnectionTracker {
	return r.connTracker
}

// Metrics returns the shared upstream metrics.
func (r *Router) Metrics() *lb.UpstreamMetrics {
	return r.metrics
}

// CircuitBreaker returns the shared circuit breaker.
func (r *Router) CircuitBreaker() *lb.CircuitBreaker {
	return r.circuitBreaker
}

// HealthChecker returns the shared health checker.
func (r *Router) HealthChecker() *lb.HealthChecker {
	return r.healthChecker
}

// ActiveModels returns canonical names that are actually routable — i.e.
// the catalog entry is active AND at least one active route lists the name
// AND at least one active upstream in that route's group lists the name in
// supported_models. Output contract is unchanged for /v1/models.
func (r *Router) ActiveModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var models []string
	for _, route := range r.routes {
		if route.Status != "active" {
			continue
		}
		g, ok := r.groups[route.UpstreamGroupID]
		if !ok {
			continue
		}
		for _, name := range route.ModelNames {
			if seen[name] {
				continue
			}
			if r.catalog != nil {
				m, ok := r.catalog.Get(name)
				if !ok || m.Status != types.ModelStatusActive {
					continue
				}
			}
			supported := false
			for _, mem := range g.members {
				if mem.upstream.Status != types.UpstreamStatusActive {
					continue
				}
				if slices.Contains(mem.upstream.SupportedModels, name) {
					supported = true
					break
				}
			}
			if !supported {
				continue
			}
			seen[name] = true
			models = append(models, name)
		}
	}
	return models
}

// decryptUpstreamKeys decrypts all upstream API keys and returns a map of upstreamID -> plaintext.
func decryptUpstreamKeys(upstreams []types.Upstream, encKey []byte, logger *slog.Logger) map[string]string {
	keys := make(map[string]string, len(upstreams))
	if len(encKey) == 0 {
		return keys
	}
	for _, u := range upstreams {
		if len(u.APIKeyEncrypted) == 0 {
			continue
		}
		plaintext, err := crypto.Decrypt(encKey, u.APIKeyEncrypted)
		if err != nil {
			if logger != nil {
				logger.Error("failed to decrypt upstream key at load time", "upstream_id", u.ID, "error", err)
			}
			continue
		}
		keys[u.ID] = string(plaintext)
	}
	return keys
}
