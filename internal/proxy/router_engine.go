package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/proxy/lb"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// sessionBinding tracks which upstream a session (trace) is pinned to.
type sessionBinding struct {
	upstreamID string
	usedAt     time.Time
}

// matchModel checks if a model name matches a glob pattern.
func matchModel(pattern, model string) bool {
	if pattern == "*" {
		return true
	}
	matched, _ := filepath.Match(pattern, model)
	return matched
}

// modelSupported checks if a model is in the supported list.
func modelSupported(supported []string, model string) bool {
	for _, s := range supported {
		if s == model {
			return true
		}
	}
	return false
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

	sessionMap sync.Map // sessionID -> sessionBinding
	sessionTTL time.Duration

	vertexTokenManager *VertexTokenManager

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
func NewRouter(
	upstreams []types.Upstream,
	groups []store.UpstreamGroupWithMembers,
	routes []types.Route,
	encKey []byte,
	logger *slog.Logger,
	sessionTTL time.Duration,
	oauthMgr *OAuthTokenManager,
) *Router {
	r := &Router{
		sessionTTL: sessionTTL,
		logger:     logger,
		oauthMgr:   oauthMgr,
	}

	// Create the Vertex AI token manager.
	r.vertexTokenManager = NewVertexTokenManager()

	// Wire the token manager into the already-registered VertexTransformer.
	SetVertexTokenManager(r.vertexTokenManager)

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
			if u.Provider == types.ProviderVertex {
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
// It checks project-specific routes first, then global routes, then auto-discovers
// upstreams that support the model.
func (r *Router) Match(projectID, model string) (*resolvedGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. Try project-specific routes (highest match_priority first, routes are pre-sorted).
	for _, route := range r.routes {
		if route.Status != "active" {
			continue
		}
		if route.ProjectID == projectID && matchModel(route.ModelPattern, model) {
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
		if route.ProjectID == "" && matchModel(route.ModelPattern, model) {
			if g, ok := r.groups[route.UpstreamGroupID]; ok {
				return g, nil
			}
		}
	}

	// 3. Auto-discover: find all active upstreams supporting the model,
	//    create a virtual group with weighted_random LB.
	var members []groupMember
	for _, u := range r.upstreams {
		if u.Status == types.UpstreamStatusActive && modelSupported(u.SupportedModels, model) {
			members = append(members, groupMember{upstream: u, weight: u.Weight, isBackup: false})
		}
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("no upstreams available for model %s", model)
	}

	return &resolvedGroup{
		group:   types.UpstreamGroup{LBPolicy: types.LBPolicyWeightedRandom},
		members: members,
	}, nil
}

// SelectWithRetry returns an ordered list of upstreams to try for the given group.
// The first element is the primary pick; subsequent elements are retry fallbacks.
// Filtering applies: open circuits, HealthDown, MaxConcurrent at capacity, and
// draining upstreams are excluded. HealthDegraded upstreams have their effective
// weight reduced to 25% (minimum 1).
func (r *Router) SelectWithRetry(ctx context.Context, group *resolvedGroup, sessionID string) []*SelectedUpstream {
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

	// 6. Apply session stickiness BEFORE balancer selection: if the session
	//    is bound to an upstream that's in our candidate list, force it as
	//    the primary pick and use the balancer only for retry slots.
	//    This must happen before SelectN because SelectN(n=1) may randomly
	//    pick a different upstream, silently breaking stickiness.
	if sessionID != "" {
		if val, ok := r.sessionMap.Load(sessionID); ok {
			binding := val.(sessionBinding)
			if time.Since(binding.usedAt) < r.sessionTTL {
				for i, c := range candidates {
					if c.Upstream.ID == binding.upstreamID {
						// Refresh binding.
						r.sessionMap.Store(sessionID, sessionBinding{
							upstreamID: c.Upstream.ID,
							usedAt:     time.Now(),
						})

						// Build result: bound upstream first.
						result := make([]*SelectedUpstream, 0, n)
						result = append(result, &SelectedUpstream{
							Upstream: c.Upstream,
							APIKey:   r.decryptedKeys[c.Upstream.ID],
						})

						// For retry slots, use balancer on remaining candidates.
						if n > 1 && len(candidates) > 1 {
							remaining := make([]lb.CandidateInfo, 0, len(candidates)-1)
							remaining = append(remaining, candidates[:i]...)
							remaining = append(remaining, candidates[i+1:]...)

							balancer, ok := r.balancers[group.group.ID]
							if !ok {
								balancer = lb.NewBalancer(group.group.LBPolicy, r.connTracker)
							}
							for _, u := range balancer.SelectN(remaining, n-1) {
								result = append(result, &SelectedUpstream{
									Upstream: u,
									APIKey:   r.decryptedKeys[u.ID],
								})
							}
						}
						return result
					}
				}
				// Bound upstream not in candidates (disabled/unhealthy) — fall through.
			} else {
				// Expired binding.
				r.sessionMap.Delete(sessionID)
			}
		}
	}

	// 7. Use group's balancer to rank (SelectN).
	//    Fall back to a default weighted_random balancer for auto-discovered groups.
	balancer, ok := r.balancers[group.group.ID]
	if !ok {
		balancer = lb.NewBalancer(group.group.LBPolicy, r.connTracker)
	}

	ranked := balancer.SelectN(candidates, n)

	// 8. Wrap as []*SelectedUpstream with decrypted API keys.
	result := make([]*SelectedUpstream, len(ranked))
	for i, u := range ranked {
		result[i] = &SelectedUpstream{
			Upstream: u,
			APIKey:   r.decryptedKeys[u.ID],
		}
	}

	return result
}

// BindSession stores a session-to-upstream binding for session stickiness.
func (r *Router) BindSession(sessionID, upstreamID string) {
	if sessionID == "" {
		return
	}
	r.sessionMap.Store(sessionID, sessionBinding{upstreamID: upstreamID, usedAt: time.Now()})
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

// ActiveModels returns all supported models from active upstreams.
func (r *Router) ActiveModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var models []string
	for _, u := range r.upstreams {
		if u.Status == types.UpstreamStatusActive {
			for _, m := range u.SupportedModels {
				if !seen[m] {
					seen[m] = true
					models = append(models, m)
				}
			}
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
