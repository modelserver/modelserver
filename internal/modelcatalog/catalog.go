// Package modelcatalog keeps an in-memory view of the global model catalog.
// It is the lookup path for every ingress request and every admin validation.
// The catalog is constructed at server start, swapped on admin writes to the
// `models` table, and independent of the router's upstream/route view.
package modelcatalog

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelserver/modelserver/internal/types"
)

// Catalog is the read-only interface callers use to resolve names and
// inspect the set of registered models.
type Catalog interface {
	// Lookup resolves a client-supplied name (canonical or alias). The
	// input is lowercased before lookup. Returns (nil, false) when the
	// name is unknown. Disabled models ARE returned — callers enforce
	// status policy themselves.
	Lookup(name string) (*types.Model, bool)

	// Get fetches by canonical name. The caller must have already
	// normalized the name (no alias resolution, no case folding here).
	Get(canonical string) (*types.Model, bool)

	// NormalizeNames maps a slice of names (canonical or alias) to their
	// canonical forms, preserving order and duplicates. If any element is
	// unknown, returns (nil, *UnknownModelsError) listing every unknown
	// in a single error so the admin UI can surface all of them at once.
	NormalizeNames(names []string) ([]string, error)

	// Snapshot returns a stable copy of the catalog at the moment of call,
	// safe to iterate without locks.
	Snapshot() []types.Model

	// Swap atomically replaces the in-memory view with the given slice.
	Swap(models []types.Model)

	// Stats returns the counters that would normally drive Prometheus
	// metrics (alias hit rate, unknown-name cardinality). Bounded at
	// maxUnknownCardinality distinct unknown raws per process lifetime.
	Stats() Stats
}

// UnknownModelsError is returned from NormalizeNames when one or more input
// names do not appear in the catalog. `Names` is the deduped, sorted list
// of every unknown name seen.
type UnknownModelsError struct {
	Names []string
}

func (e *UnknownModelsError) Error() string {
	return fmt.Sprintf("unknown model name(s): %s", strings.Join(e.Names, ", "))
}

// Stats is the current snapshot of lookup counters.
type Stats struct {
	// AliasResolved maps alias → count of resolved lookups. An alias hit
	// is any Lookup where the input was not the canonical name.
	AliasResolved map[string]int64
	// Unknown maps raw input → count of Lookup calls that returned no
	// result. Bounded per the cardinality cap in the implementation.
	Unknown map[string]int64
}

// maxUnknownCardinality caps the per-process number of distinct unknown raw
// inputs we remember, so adversarial clients cannot blow up metric labels.
const maxUnknownCardinality = 200

type impl struct {
	mu       sync.RWMutex
	index    map[string]*types.Model // lowercased alias/name → canonical row
	rows     []types.Model           // ordered by name; returned by Snapshot

	aliasHits sync.Map // alias(string) → *atomic.Int64
	unknown   sync.Map // raw(string) → *atomic.Int64
	unknownN  atomic.Int32
}

// New returns a Catalog populated from the given slice. A nil/empty slice
// is legal — callers start empty on first boot and Swap after the DB load.
func New(models []types.Model) Catalog {
	c := &impl{}
	c.Swap(models)
	return c
}

func (c *impl) Swap(models []types.Model) {
	rows := make([]types.Model, len(models))
	copy(rows, models)
	idx := make(map[string]*types.Model, len(rows)*2)
	for i := range rows {
		row := &rows[i]
		idx[strings.ToLower(row.Name)] = row
		for _, a := range row.Aliases {
			idx[strings.ToLower(a)] = row
		}
	}
	c.mu.Lock()
	c.index = idx
	c.rows = rows
	c.mu.Unlock()
}

func (c *impl) Snapshot() []types.Model {
	c.mu.RLock()
	out := make([]types.Model, len(c.rows))
	copy(out, c.rows)
	c.mu.RUnlock()
	return out
}

func (c *impl) Lookup(name string) (*types.Model, bool) {
	lc := strings.ToLower(name)
	c.mu.RLock()
	m, ok := c.index[lc]
	c.mu.RUnlock()
	if !ok {
		c.bumpUnknown(lc)
		return nil, false
	}
	if lc != strings.ToLower(m.Name) {
		c.bumpAlias(lc)
	}
	return m, true
}

func (c *impl) Get(canonical string) (*types.Model, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.index[strings.ToLower(canonical)]
	if !ok || !strings.EqualFold(m.Name, canonical) {
		return nil, false
	}
	return m, true
}

func (c *impl) NormalizeNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(names))
	var unknownSet map[string]struct{}
	c.mu.RLock()
	for _, raw := range names {
		if m, ok := c.index[strings.ToLower(raw)]; ok {
			out = append(out, m.Name)
			continue
		}
		if unknownSet == nil {
			unknownSet = make(map[string]struct{})
		}
		unknownSet[raw] = struct{}{}
	}
	c.mu.RUnlock()
	if len(unknownSet) > 0 {
		unknown := make([]string, 0, len(unknownSet))
		for n := range unknownSet {
			unknown = append(unknown, n)
		}
		// Stable order so tests and error messages are reproducible.
		sortStrings(unknown)
		return nil, &UnknownModelsError{Names: unknown}
	}
	return out, nil
}

func (c *impl) Stats() Stats {
	stats := Stats{
		AliasResolved: make(map[string]int64),
		Unknown:       make(map[string]int64),
	}
	c.aliasHits.Range(func(k, v any) bool {
		stats.AliasResolved[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	c.unknown.Range(func(k, v any) bool {
		stats.Unknown[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return stats
}

func (c *impl) bumpAlias(key string) {
	if v, ok := c.aliasHits.Load(key); ok {
		v.(*atomic.Int64).Add(1)
		return
	}
	var n atomic.Int64
	n.Add(1)
	actual, loaded := c.aliasHits.LoadOrStore(key, &n)
	if loaded {
		actual.(*atomic.Int64).Add(1)
	}
}

func (c *impl) bumpUnknown(key string) {
	if v, ok := c.unknown.Load(key); ok {
		v.(*atomic.Int64).Add(1)
		return
	}
	if c.unknownN.Load() >= maxUnknownCardinality {
		return
	}
	var n atomic.Int64
	n.Add(1)
	actual, loaded := c.unknown.LoadOrStore(key, &n)
	if loaded {
		actual.(*atomic.Int64).Add(1)
		return
	}
	c.unknownN.Add(1)
}

// Tiny local sort to avoid importing "sort" for one call. Keeps the package
// import graph tight (modelcatalog is called from hot paths).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
