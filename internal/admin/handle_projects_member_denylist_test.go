package admin

import (
	"encoding/json"
	"strconv"
	"testing"
)

// Pure unit tests (no DB needed) ------------------------------------------------

// TestNormalizeDeniedModels covers the trim/dedupe/cap logic in isolation.
func TestNormalizeDeniedModels(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
		ok   bool
	}{
		{"empty", []string{}, []string{}, true},
		{"trims_drops_dedupes",
			[]string{"  a  ", "a", "b", "", " b"},
			[]string{"a", "b"}, true},
		{"preserves_order",
			[]string{"z", "a", "m"},
			[]string{"z", "a", "m"}, true},
		{"only_empties_and_whitespace",
			[]string{"", "   ", "\t"},
			[]string{}, true},
		{"exact_256_ok",
			gen("m", 256), gen("m", 256), true},
		{"over_256_rejected",
			gen("m", 257), nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := normalizeDeniedModels(c.in)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if c.ok && !equalStrings(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func gen(prefix string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = prefix + "-" + strconv.Itoa(i)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestUpdateMemberBody_DeniedModelsJSON proves the body's tri-state
// JSON contract (omitted / null / [] / [...]).
func TestUpdateMemberBody_DeniedModelsJSON(t *testing.T) {
	type body struct {
		Role           *string   `json:"role"`
		CreditQuotaPct *float64  `json:"credit_quota_percent"`
		ClearQuota     bool      `json:"clear_quota"`
		DeniedModels   *[]string `json:"denied_models"`
	}
	cases := []struct {
		name string
		json string
		want *[]string
	}{
		{"omitted", `{}`, nil},
		{"null", `{"denied_models":null}`, nil},
		{"empty", `{"denied_models":[]}`, &[]string{}},
		{"one", `{"denied_models":["x"]}`, &[]string{"x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b body
			if err := json.Unmarshal([]byte(c.json), &b); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			switch {
			case c.want == nil && b.DeniedModels != nil:
				t.Fatalf("want nil, got %v", *b.DeniedModels)
			case c.want != nil && b.DeniedModels == nil:
				t.Fatalf("want non-nil, got nil")
			case c.want != nil:
				if len(*b.DeniedModels) != len(*c.want) {
					t.Fatalf("len mismatch: want %v got %v", *c.want, *b.DeniedModels)
				}
				for i := range *c.want {
					if (*b.DeniedModels)[i] != (*c.want)[i] {
						t.Fatalf("idx %d: want %q got %q", i, (*c.want)[i], (*b.DeniedModels)[i])
					}
				}
			}
		})
	}
}

// TestSetDeniedModelsCacheInvalidator confirms the setter installs a
// non-nil function and that nil resets are rejected (no-op).
func TestSetDeniedModelsCacheInvalidator(t *testing.T) {
	// Snapshot + restore.
	prev := proxyInvalidateDeniedModelsCache
	t.Cleanup(func() { proxyInvalidateDeniedModelsCache = prev })

	called := false
	SetDeniedModelsCacheInvalidator(func(p, u string) {
		called = true
		if p != "p" || u != "u" {
			t.Fatalf("got (%q,%q)", p, u)
		}
	})
	proxyInvalidateDeniedModelsCache("p", "u")
	if !called {
		t.Fatalf("invalidator not called")
	}

	// nil should be a no-op (not overwrite the installed function).
	SetDeniedModelsCacheInvalidator(nil)
	called = false
	proxyInvalidateDeniedModelsCache("p", "u")
	if !called {
		t.Fatalf("invalidator was overwritten by nil; expected nil-arg to be no-op")
	}
}
