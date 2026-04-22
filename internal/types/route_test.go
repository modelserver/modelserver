package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoute_JSONIncludesRequestKinds(t *testing.T) {
	r := Route{
		ID:           "r1",
		ModelNames:   []string{"m"},
		RequestKinds: []string{KindAnthropicMessages, KindAnthropicCountTokens},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"request_kinds":["anthropic_messages","anthropic_count_tokens"]`
	if !strings.Contains(string(b), want) {
		t.Errorf("missing %s in JSON: %s", want, b)
	}
}

func TestRoute_JSONUnmarshalRoundtripsRequestKinds(t *testing.T) {
	in := Route{ID: "r1", RequestKinds: []string{KindOpenAIResponses}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Route
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.RequestKinds) != 1 || out.RequestKinds[0] != KindOpenAIResponses {
		t.Errorf("RequestKinds roundtrip = %v, want [%q]", out.RequestKinds, KindOpenAIResponses)
	}
}

func TestRoute_EmptyRequestKindsSerialisesAsEmptyArray(t *testing.T) {
	// No `omitempty` — the dashboard distinguishes "not set" (key absent) from
	// "explicitly empty" (key present with []). Server-side validation rejects
	// the latter, but absence vs empty must be visible on the wire.
	r := Route{ID: "r1"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"request_kinds":null`) && !strings.Contains(string(b), `"request_kinds":[]`) {
		t.Errorf("request_kinds key absent — should serialise even when empty: %s", b)
	}
}
