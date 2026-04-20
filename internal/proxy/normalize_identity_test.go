package proxy

import (
	"bytes"
	"testing"
)

func TestRecomputeCCH_CrossValidatedWithPythonPOC(t *testing.T) {
	// These test vectors were independently computed using the Python POC from
	// https://a10k.co/b/reverse-engineering-claude-code-cch.html:
	//
	//   import xxhash
	//   CCH_SEED = 0x6E52736AC806831E
	//   cch = format(xxhash.xxh64(body.encode(), seed=CCH_SEED).intdigest() & 0xFFFFF, "05x")
	//
	// Each body contains cch=00000 as the placeholder (hash is computed over
	// the placeholder, then the result replaces "cch=00000" in the final output).

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "full_attribution_header",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`,
			want: "55875",
		},
		{
			name: "different_cc_version",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.abc; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`,
			want: "df769",
		},
		{
			name: "minimal_body",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cch=00000;"}],"messages":[]}`,
			want: "96fa3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Start with a body that has a different cch value (simulating
			// an incoming request whose cch is stale after body modification).
			incoming := cchRe.ReplaceAll([]byte(tc.body), []byte("cch=fffff;"))

			result := recomputeCCH(incoming)

			loc := cchRe.FindIndex(result)
			if loc == nil {
				t.Fatal("result should contain a cch field")
			}
			got := string(result[loc[0]+4 : loc[1]-1])

			if got != tc.want {
				t.Errorf("cch = %s, want %s (cross-validated with Python POC)", got, tc.want)
			}
		})
	}
}

func TestRecomputeCCH_NoCCHField(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[]}`)
	result := recomputeCCH(body)
	if string(result) != string(body) {
		t.Error("recomputeCCH should not modify body without cch field")
	}
}

func TestRecomputeCCH_Idempotent(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=00000;"}],"model":"test","messages":[]}`)

	first := recomputeCCH(body)
	second := recomputeCCH(first)

	loc1 := cchRe.FindIndex(first)
	loc2 := cchRe.FindIndex(second)
	cch1 := string(first[loc1[0]+4 : loc1[1]-1])
	cch2 := string(second[loc2[0]+4 : loc2[1]-1])

	if cch1 != cch2 {
		t.Errorf("not idempotent: first=%s, second=%s", cch1, cch2)
	}
	if cch1 == "00000" {
		t.Error("cch should not remain 00000 after recomputation")
	}
}

// Bodies for ValidateCCH tests reuse POC-cross-validated vectors from
// TestRecomputeCCH_CrossValidatedWithPythonPOC: a body whose placeholdered
// form (cch=00000;) hashes to value X is a "correct" body when it carries
// cch=X itself.
//
// Full attribution header vector → expected hash "55875".
const cchMatchBody = `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=55875;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`

func TestValidateCCH_Match(t *testing.T) {
	status, client, expected := ValidateCCH([]byte(cchMatchBody))
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want %q", status, CCHStatusMatch)
	}
	if client != "55875" {
		t.Errorf("client = %q, want %q", client, "55875")
	}
	if expected != "55875" {
		t.Errorf("expected = %q, want %q", expected, "55875")
	}
}

func TestValidateCCH_Mismatch(t *testing.T) {
	// Replace the correct cch "55875" with a wrong value.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=deadb;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	status, client, expected := ValidateCCH(body)
	if status != CCHStatusMismatch {
		t.Errorf("status = %q, want %q", status, CCHStatusMismatch)
	}
	if client != "deadb" {
		t.Errorf("client = %q, want %q", client, "deadb")
	}
	if expected != "55875" {
		t.Errorf("expected = %q, want %q", expected, "55875")
	}
}

func TestValidateCCH_Absent(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[]}`)
	status, client, expected := ValidateCCH(body)
	if status != CCHStatusAbsent {
		t.Errorf("status = %q, want %q", status, CCHStatusAbsent)
	}
	if client != "" || expected != "" {
		t.Errorf("client/expected should be empty, got %q / %q", client, expected)
	}
}

func TestValidateCCH_AbsentAttributionNoCCH(t *testing.T) {
	// Attribution header present but cch segment missing.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli;"}],"model":"claude-3","messages":[]}`)
	status, _, _ := ValidateCCH(body)
	if status != CCHStatusAbsent {
		t.Errorf("status = %q, want %q", status, CCHStatusAbsent)
	}
}

func TestValidateCCH_CaseInsensitive(t *testing.T) {
	// "different_cc_version" POC vector: expected cch is "df769". Verify
	// ValidateCCH accepts client-supplied uppercase "DF769" as a match.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.abc; cc_entrypoint=cli; cch=DF769;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	status, client, expected := ValidateCCH(body)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want %q (case-insensitive match)", status, CCHStatusMatch)
	}
	if client != "DF769" {
		t.Errorf("client = %q, want %q", client, "DF769")
	}
	if expected != "df769" {
		t.Errorf("expected = %q, want %q", expected, "df769")
	}
}

func TestValidateCCH_NoMutation(t *testing.T) {
	body := []byte(cchMatchBody)
	snapshot := append([]byte{}, body...)
	ValidateCCH(body)
	if !bytes.Equal(body, snapshot) {
		t.Error("ValidateCCH must not mutate the input body")
	}
}
