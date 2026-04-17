package modelcatalog

import (
	"reflect"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestSuggest_RanksByDistanceThenAlpha(t *testing.T) {
	c := New([]types.Model{
		{Name: "claude-opus-4-7", Aliases: []string{"claude-opus-latest"}},
		{Name: "claude-opus-4-6"},
		{Name: "claude-sonnet-4-6"},
	})
	got := Suggest(c, "claude-opus-4-8", 2, 3)
	want := []string{"claude-opus-4-6", "claude-opus-4-7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSuggest_HonorsCap(t *testing.T) {
	c := New([]types.Model{
		{Name: "abc"},
		{Name: "abd"},
		{Name: "abe"},
		{Name: "abf"},
	})
	got := Suggest(c, "abc", 2, 2)
	if len(got) != 2 {
		t.Fatalf("expected at most 2, got %v", got)
	}
}

func TestSuggest_ExceedingMaxDistanceExcluded(t *testing.T) {
	c := New([]types.Model{{Name: "completely-different-name"}})
	got := Suggest(c, "abc", 2, 3)
	if len(got) != 0 {
		t.Fatalf("expected no suggestions, got %v", got)
	}
}
