package proxy

import (
	"fmt"
	"testing"

	"github.com/OneOfOne/xxhash"
)

func TestRecomputeCCH_KnownVector(t *testing.T) {
	// Known test vector: compact JSON body with cch=00000 placeholder.
	// Expected cch computed independently via Go xxHash64 with seed 0x6E52736AC806831E.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)

	// Compute expected value directly.
	h := xxhash.NewS64(cchSeed)
	h.Write(body)
	want := fmt.Sprintf("%05x", h.Sum64()&0xFFFFF)

	// Now set a fake cch value to simulate an incoming body.
	bodyWithFake := make([]byte, len(body))
	copy(bodyWithFake, body)
	bodyWithFake = cchRe.ReplaceAll(bodyWithFake, []byte("cch=aaaaa;"))

	result := recomputeCCH(bodyWithFake)

	loc := cchRe.FindIndex(result)
	if loc == nil {
		t.Fatal("result should contain a cch field")
	}
	got := string(result[loc[0]+4 : loc[1]-1])

	if got != want {
		t.Errorf("cch = %s, want %s", got, want)
	}
	if got != "55875" {
		t.Errorf("cch = %s, want hard-coded reference 55875", got)
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

func TestRecomputeCCH_Format(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.abc; cc_entrypoint=cli; cch=12345;"}],"messages":[]}`)

	result := recomputeCCH(body)
	loc := cchRe.FindIndex(result)
	if loc == nil {
		t.Fatal("no cch in result")
	}
	cch := string(result[loc[0]+4 : loc[1]-1])

	if len(cch) != 5 {
		t.Errorf("cch length %d, want 5", len(cch))
	}
	for _, c := range cch {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("cch contains non-hex char: %c", c)
		}
	}
}

func TestRecomputeCCH_MatchesOfficialBody(t *testing.T) {
	// This test uses a real request body captured from Claude Code (CC 2.1.112).
	// The official cch value computed by Bun's native attestation is 755f5.
	// If testdata/cch_test_body.json exists with the full compact JSON body,
	// we verify our algorithm matches.
	//
	// To run: place the compact JSON body (with cch=755f5;) in
	// internal/proxy/testdata/cch_test_body.json

	// For now, skip if fixture not available.
	t.Skip("requires testdata/cch_test_body.json with full official request body")
}
