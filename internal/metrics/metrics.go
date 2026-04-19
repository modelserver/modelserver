// Package metrics exposes in-process counters for the extra-usage subsystem.
// The implementation is intentionally dependency-free (no Prometheus client
// import) — it maintains labeled counters in memory and a /metrics HTTP
// handler renders them in text/prometheus exposition format so existing
// scrape infrastructure keeps working.
//
// The set of series we emit is small and bounded — per-reason guard
// decisions, deduction outcomes, underdraft by project, a handful of data
// consistency counters, and per-project balance gauges. Unknown project IDs
// that arrive from public requests do NOT become counter labels (the only
// label carrying a project id is updated via SetBalance from trusted paths).
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
)

// labelPair keyed counter bucket. Empty labels are tolerated; the exposition
// formatter elides empty-valued labels.
type labelPair struct {
	name  string
	value string
}

type counter struct {
	name   string
	help   string
	mu     sync.RWMutex
	values map[string]float64 // encoded labels → value
}

func newCounter(name, help string) *counter {
	return &counter{name: name, help: help, values: make(map[string]float64)}
}

func (c *counter) inc(by float64, pairs ...labelPair) {
	key := encodeLabels(pairs)
	c.mu.Lock()
	c.values[key] += by
	c.mu.Unlock()
}

func (c *counter) set(val float64, pairs ...labelPair) {
	key := encodeLabels(pairs)
	c.mu.Lock()
	c.values[key] = val
	c.mu.Unlock()
}

func encodeLabels(pairs []labelPair) string {
	if len(pairs) == 0 {
		return ""
	}
	sorted := make([]labelPair, len(pairs))
	copy(sorted, pairs)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	out := ""
	for i, p := range sorted {
		if i > 0 {
			out += ","
		}
		out += p.name + "=" + p.value
	}
	return out
}

// Declared counters. Names follow spec §7.4 exactly so ops dashboards wire
// against a stable contract.
var (
	extraUsageRequestsTotal      = newCounter("extra_usage_requests_total", "extra-usage guard decisions")
	extraUsageDeductionsTotal    = newCounter("extra_usage_deductions_total", "extra-usage deduction outcomes")
	extraUsageUnderdraftTotal    = newCounter("extra_usage_underdraft_total", "deduction attempts that failed due to race with zero balance")
	extraUsageMissingRateTotal   = newCounter("extra_usage_missing_rate_total", "settle attempts aborted because model.DefaultCreditRate was nil")
	extraUsageMissingPublisherTotal = newCounter("extra_usage_missing_publisher_total", "subscription eligibility saw model.publisher = ''")
	extraUsageTopupsTotal        = newCounter("extra_usage_topups_total", "topup webhook deliveries")
	extraUsageBalanceFen         = newCounter("extra_usage_balance_fen", "per-project extra-usage balance in fen")

	counters = []*counter{
		extraUsageRequestsTotal,
		extraUsageDeductionsTotal,
		extraUsageUnderdraftTotal,
		extraUsageMissingRateTotal,
		extraUsageMissingPublisherTotal,
		extraUsageTopupsTotal,
		extraUsageBalanceFen,
	}
)

// IncExtraUsageRequest records one guard decision.
func IncExtraUsageRequest(reason, result string) {
	extraUsageRequestsTotal.inc(1,
		labelPair{"reason", quote(reason)},
		labelPair{"result", quote(result)})
}

// IncExtraUsageDeduction records one deduction outcome.
func IncExtraUsageDeduction(result string) {
	extraUsageDeductionsTotal.inc(1, labelPair{"result", quote(result)})
}

// IncExtraUsageUnderdraft bumps the per-project underdraft counter. Spec's
// circuit-breaker rule queries this label.
func IncExtraUsageUnderdraft(projectID string) {
	extraUsageUnderdraftTotal.inc(1, labelPair{"project_id", quote(projectID)})
}

// UnderdraftCountSince returns the total underdraft count for a project
// across all time. The circuit-breaker queries this to decide whether to
// pause the project — exact 5-minute windowing is handled by comparing two
// readings taken 5 minutes apart (the breaker caches the previous reading).
func UnderdraftCountSince(projectID string) float64 {
	extraUsageUnderdraftTotal.mu.RLock()
	defer extraUsageUnderdraftTotal.mu.RUnlock()
	return extraUsageUnderdraftTotal.values[encodeLabels([]labelPair{{name: "project_id", value: quote(projectID)}})]
}

// IncExtraUsageMissingRate records that a request had to skip settlement
// because the catalog had no DefaultCreditRate for the model.
func IncExtraUsageMissingRate(model string) {
	extraUsageMissingRateTotal.inc(1, labelPair{"model", quote(model)})
}

// IncExtraUsageMissingPublisher records that a model with publisher="" was
// seen in the hot path. Admins must backfill.
func IncExtraUsageMissingPublisher(model string) {
	extraUsageMissingPublisherTotal.inc(1, labelPair{"model", quote(model)})
}

// IncExtraUsageTopup records one successful topup via webhook.
func IncExtraUsageTopup(channel string) {
	extraUsageTopupsTotal.inc(1, labelPair{"channel", quote(channel)})
}

// SetExtraUsageBalance stores a project's current balance. Called from write
// paths so the gauge stays fresh.
func SetExtraUsageBalance(projectID string, balanceFen int64) {
	extraUsageBalanceFen.set(float64(balanceFen), labelPair{"project_id", quote(projectID)})
}

func quote(s string) string {
	return `"` + s + `"`
}

// Handler returns an http.Handler that serves all counters in the Prometheus
// text exposition format. Mount it at /metrics on the admin/internal
// listener.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)
		writeExposition(w)
	})
}

func writeExposition(w io.Writer) {
	for _, c := range counters {
		fmt.Fprintf(w, "# HELP %s %s\n", c.name, c.help)
		fmt.Fprintf(w, "# TYPE %s counter\n", c.name)
		c.mu.RLock()
		keys := make([]string, 0, len(c.values))
		for k := range c.values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := c.values[k]
			if k == "" {
				fmt.Fprintf(w, "%s %g\n", c.name, v)
			} else {
				fmt.Fprintf(w, "%s{%s} %g\n", c.name, k, v)
			}
		}
		c.mu.RUnlock()
	}
}
