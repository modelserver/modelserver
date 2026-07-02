package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestModelMetadata_ExtraUsageOnlyJSONRoundTrip locks in the wire name of
// ExtraUsageOnly. SubscriptionEligibilityMiddleware reads this field to
// force premium models (fable-5) onto the extra-usage path; if the JSON
// tag drifts (typo, capitalization change) the DB row round-trip through
// scanModel silently loses the flag and Claude Code subscribers begin
// consuming a model priced above their plan bundle.
//
// Guard three things:
//  1. The wire key is exactly "extra_usage_only" (what the migration writes
//     and what the admin UI documents).
//  2. Marshal→Unmarshal preserves the value.
//  3. omitempty holds — the zero value produces no wire key, so existing
//     rows without the field stay indistinguishable from ExtraUsageOnly=false.
func TestModelMetadata_ExtraUsageOnlyJSONRoundTrip(t *testing.T) {
	// (1) Wire-key check: marshaling the flag ON must produce the exact key
	// the migration and the middleware agree on.
	on := ModelMetadata{ExtraUsageOnly: true}
	b, err := json.Marshal(on)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"extra_usage_only":true`) {
		t.Fatalf("marshaled metadata missing extra_usage_only:true; got %s", got)
	}

	// (2) Round-trip: what the DB stores must survive re-parsing.
	var back ModelMetadata
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.ExtraUsageOnly {
		t.Fatalf("round-trip dropped ExtraUsageOnly; got %+v", back)
	}

	// (3) omitempty: the zero value must produce no key at all, so rows
	// written before the field existed (or with the flag off) don't emit
	// "extra_usage_only":false and pollute grep/audits.
	off, err := json.Marshal(ModelMetadata{})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(off), "extra_usage_only") {
		t.Fatalf("zero-value ModelMetadata emitted extra_usage_only key; got %s", off)
	}

	// (4) A row with only the extra_usage_only field set must unmarshal
	// with the flag on — this is the shape migration 058 writes for
	// fable-5 (plus category).
	const rowJSON = `{"category":"chat","extra_usage_only":true}`
	var row ModelMetadata
	if err := json.Unmarshal([]byte(rowJSON), &row); err != nil {
		t.Fatalf("unmarshal migration shape: %v", err)
	}
	if !row.ExtraUsageOnly || row.Category != "chat" {
		t.Fatalf("migration-shape round-trip: got %+v, want ExtraUsageOnly=true Category=chat", row)
	}
}
