package proxy

import (
	"fmt"
	"os"
	"testing"

	"github.com/OneOfOne/xxhash"
)

// TestCCH_RoundTrip_WithRealWireBody runs an end-to-end check against a
// real wire body captured via LD_PRELOAD. Skipped unless CCH_SAMPLE_BODY
// env var points to a captured HTTP request file (headers + \r\n\r\n + body).
func TestCCH_RoundTrip_WithRealWireBody(t *testing.T) {
	path := os.Getenv("CCH_SAMPLE_BODY")
	if path == "" {
		t.Skip("set CCH_SAMPLE_BODY to a captured HTTP request file to run")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := raw
	for i := 0; i < len(raw)-3; i++ {
		if raw[i] == '\r' && raw[i+1] == '\n' && raw[i+2] == '\r' && raw[i+3] == '\n' {
			body = raw[i+4:]
			break
		}
	}

	m := cchRe.FindSubmatch(body)
	if m == nil {
		t.Fatal("no cch= in body")
	}
	origCCH := string(m[2])

	// Path 1: ValidateCCH must return match for real CLI body.
	status, client, expected := ValidateCCH(body)
	t.Logf("ValidateCCH: status=%s client=%s expected=%s", status, client, expected)
	if status != CCHStatusMatch {
		t.Errorf("real CLI body should validate, got status=%s (client=%s expected=%s)",
			status, client, expected)
	}

	// Path 2: recomputeCCH on unmodified body must reproduce the original cch.
	reSigned := recomputeCCH(body)
	m2 := cchRe.FindSubmatch(reSigned)
	if m2 == nil {
		t.Fatal("re-signed body has no cch")
	}
	newCCH := string(m2[2])
	t.Logf("recomputeCCH: original=%s re-signed=%s", origCCH, newCCH)
	if newCCH != origCCH {
		t.Errorf("round-trip mismatch: original=%s re-signed=%s", origCCH, newCCH)
	}

	// Path 3: Simulate the full normalize flow with a custom device_id and
	// verify the resulting cch validates against the modified body.
	testDeviceID := DeriveClaudeCodeDeviceID("test-upstream-xyz")
	modified := normalizeRequestBody(append([]byte{}, body...), testDeviceID)
	m3 := cchRe.FindSubmatch(modified)
	if m3 == nil {
		t.Fatal("modified body has no cch")
	}
	modCCH := string(m3[2])

	// Independently verify: hash the modified body with cch=00000 placeholder
	withPh := cchRe.ReplaceAll(modified, []byte("${1}00000${3}"))
	h := xxhash.NewS64(cchSeed)
	h.Write(withPh)
	independentCCH := fmt.Sprintf("%05x", h.Sum64()&0xFFFFF)

	t.Logf("full normalize: cch in body=%s, independent recompute=%s", modCCH, independentCCH)
	if modCCH != independentCCH {
		t.Errorf("normalize produced inconsistent cch: body has %s but independent recompute is %s",
			modCCH, independentCCH)
	}
}
