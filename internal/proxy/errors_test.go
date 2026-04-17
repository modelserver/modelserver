package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestWriteUnsupportedModelError_PerIngressShape pins the response envelope
// for every ingress provider. Clients written against the upstream provider's
// SDK should be able to parse the error the same way they parse other
// errors from that endpoint.
func TestWriteUnsupportedModelError_PerIngressShape(t *testing.T) {
	cases := []struct {
		ingress    string
		check      func(*testing.T, map[string]interface{})
	}{
		{
			ingress: IngressAnthropic,
			check: func(t *testing.T, body map[string]interface{}) {
				if body["type"] != "error" {
					t.Errorf("anthropic top-level type=%v want error", body["type"])
				}
				err := body["error"].(map[string]interface{})
				if err["type"] != "unsupported_model" {
					t.Errorf("anthropic error.type=%v", err["type"])
				}
				suggestions, _ := err["suggestions"].([]interface{})
				want := []interface{}{"claude-opus-4-7"}
				if !reflect.DeepEqual(suggestions, want) {
					t.Errorf("suggestions=%v, want %v", suggestions, want)
				}
			},
		},
		{
			ingress: IngressOpenAI,
			check: func(t *testing.T, body map[string]interface{}) {
				err := body["error"].(map[string]interface{})
				if err["type"] != "unsupported_model" || err["code"] != "unsupported_model" {
					t.Errorf("openai err.type=%v code=%v", err["type"], err["code"])
				}
			},
		},
		{
			ingress: IngressGemini,
			check: func(t *testing.T, body map[string]interface{}) {
				err := body["error"].(map[string]interface{})
				if err["status"] != "INVALID_ARGUMENT" {
					t.Errorf("gemini err.status=%v", err["status"])
				}
				if int(err["code"].(float64)) != 400 {
					t.Errorf("gemini err.code=%v", err["code"])
				}
				details, _ := err["details"].([]interface{})
				if len(details) != 1 {
					t.Fatalf("gemini err.details=%v", details)
				}
				d0 := details[0].(map[string]interface{})
				if _, ok := d0["suggestions"]; !ok {
					t.Errorf("gemini details[0] missing suggestions: %v", d0)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.ingress, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeUnsupportedModelError(rec, tc.ingress, "claude-opus-4-8", []string{"claude-opus-4-7"}, "unknown")
			if rec.Code != 400 {
				t.Fatalf("status=%d want 400", rec.Code)
			}
			var body map[string]interface{}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			tc.check(t, body)
		})
	}
}

func TestWriteUnsupportedModelError_DisabledMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeUnsupportedModelError(rec, IngressAnthropic, "claude-opus-4-7", nil, "disabled")
	var body map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&body)
	msg := body["error"].(map[string]interface{})["message"].(string)
	if !strings.Contains(msg, "disabled") {
		t.Errorf("disabled message should mention disabled state, got %q", msg)
	}
}
