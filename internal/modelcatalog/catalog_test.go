package modelcatalog

import (
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func fixtureModels() []types.Model {
	return []types.Model{
		{
			Name:    "claude-opus-4-7",
			Aliases: []string{"claude-opus-latest", "opus-4-7"},
			Status:  types.ModelStatusActive,
		},
		{
			Name:    "claude-sonnet-4-6",
			Aliases: []string{"sonnet-4-6"},
			Status:  types.ModelStatusActive,
		},
		{
			Name:    "claude-haiku-3",
			Aliases: nil,
			Status:  types.ModelStatusDisabled,
		},
	}
}

func TestLookup_CanonicalHit(t *testing.T) {
	c := New(fixtureModels())
	m, ok := c.Lookup("claude-opus-4-7")
	if !ok || m == nil || m.Name != "claude-opus-4-7" {
		t.Fatalf("canonical lookup failed: got %+v, ok=%v", m, ok)
	}
}

func TestLookup_AliasHit(t *testing.T) {
	c := New(fixtureModels())
	m, ok := c.Lookup("claude-opus-latest")
	if !ok || m == nil || m.Name != "claude-opus-4-7" {
		t.Fatalf("alias lookup failed: got %+v, ok=%v", m, ok)
	}
}

func TestLookup_CaseInsensitive(t *testing.T) {
	c := New(fixtureModels())
	m, ok := c.Lookup("Claude-Opus-4-7")
	if !ok || m == nil || m.Name != "claude-opus-4-7" {
		t.Fatalf("uppercase input should normalize to lowercase; got %+v, ok=%v", m, ok)
	}
	m, ok = c.Lookup("CLAUDE-OPUS-LATEST")
	if !ok || m.Name != "claude-opus-4-7" {
		t.Fatalf("uppercase alias should normalize; got %+v, ok=%v", m, ok)
	}
}

func TestLookup_Unknown(t *testing.T) {
	c := New(fixtureModels())
	if m, ok := c.Lookup("gpt-42"); ok || m != nil {
		t.Fatalf("unknown model should return (nil, false); got %+v, ok=%v", m, ok)
	}
}

func TestLookup_DisabledHit(t *testing.T) {
	c := New(fixtureModels())
	m, ok := c.Lookup("claude-haiku-3")
	if !ok || m == nil || m.Status != types.ModelStatusDisabled {
		t.Fatalf("disabled model should still be returned by Lookup so callers can decide")
	}
}

func TestGet_CanonicalOnly(t *testing.T) {
	c := New(fixtureModels())
	if _, ok := c.Get("claude-opus-latest"); ok {
		t.Fatalf("Get must only match canonical names, not aliases")
	}
	if _, ok := c.Get("claude-opus-4-7"); !ok {
		t.Fatalf("Get must match canonical names exactly")
	}
}

func TestNormalizeNames_HappyPath(t *testing.T) {
	c := New(fixtureModels())
	got, err := c.NormalizeNames([]string{"claude-opus-latest", "claude-sonnet-4-6", "claude-opus-latest"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-opus-4-7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeNames preserves order + duplicates: got %v, want %v", got, want)
	}
}

func TestNormalizeNames_ReportsAllUnknowns(t *testing.T) {
	c := New(fixtureModels())
	_, err := c.NormalizeNames([]string{"gpt-42", "claude-opus-latest", "gpt-43"})
	if err == nil {
		t.Fatalf("expected error for unknowns")
	}
	var uerr *UnknownModelsError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *UnknownModelsError, got %T (%v)", err, err)
	}
	sort.Strings(uerr.Names)
	want := []string{"gpt-42", "gpt-43"}
	if !reflect.DeepEqual(uerr.Names, want) {
		t.Fatalf("UnknownModelsError must list every unknown, deduped. got %v, want %v", uerr.Names, want)
	}
}

func TestNormalizeNames_EmptyInput(t *testing.T) {
	c := New(fixtureModels())
	got, err := c.NormalizeNames(nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("nil input should be a no-op; got %v err=%v", got, err)
	}
}

func TestSwapAndSnapshot(t *testing.T) {
	c := New(fixtureModels())
	snap1 := c.Snapshot()
	if len(snap1) != 3 {
		t.Fatalf("expected 3 models in snapshot, got %d", len(snap1))
	}

	c.Swap([]types.Model{{Name: "only-one", Status: types.ModelStatusActive}})
	snap2 := c.Snapshot()
	if len(snap2) != 1 || snap2[0].Name != "only-one" {
		t.Fatalf("Swap should replace the in-memory view; got %+v", snap2)
	}
	if len(snap1) != 3 {
		t.Fatalf("prior snapshot must be unaffected by Swap")
	}
}

func TestConcurrentReadAndSwap(t *testing.T) {
	c := New(fixtureModels())
	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					c.Lookup("claude-opus-latest")
					c.Snapshot()
				}
			}
		}()
	}

	for i := 0; i < 100; i++ {
		c.Swap(fixtureModels())
	}
	close(stop)
	wg.Wait()
}

func TestAliasResolvedCounter(t *testing.T) {
	c := New(fixtureModels())
	_, _ = c.Lookup("claude-opus-latest")
	_, _ = c.Lookup("claude-opus-latest")
	_, _ = c.Lookup("claude-opus-4-7") // canonical, must not increment
	stats := c.Stats()
	if stats.AliasResolved["claude-opus-latest"] != 2 {
		t.Fatalf("expected 2 alias hits for claude-opus-latest, got %d", stats.AliasResolved["claude-opus-latest"])
	}
}

func TestUnknownCounter(t *testing.T) {
	c := New(fixtureModels())
	_, _ = c.Lookup("gpt-42")
	_, _ = c.Lookup("gpt-42")
	stats := c.Stats()
	if stats.Unknown["gpt-42"] != 2 {
		t.Fatalf("expected 2 unknown hits for gpt-42, got %d", stats.Unknown["gpt-42"])
	}
}
