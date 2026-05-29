package proxy

import (
	"strconv"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIsInvalidEncryptedContentError(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "matches OpenAI invalid_encrypted_content",
			body: `{"error":{"message":"could not be verified","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`,
			want: true,
		},
		{
			name: "different error code does not match",
			body: `{"error":{"code":"rate_limit_exceeded"}}`,
			want: false,
		},
		{
			name: "missing error.code does not match",
			body: `{"error":{"message":"oops"}}`,
			want: false,
		},
		{
			name: "empty body does not match",
			body: ``,
			want: false,
		},
		{
			name: "non-JSON body does not match",
			body: `not json`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isInvalidEncryptedContentError([]byte(tc.body)); got != tc.want {
				t.Errorf("isInvalidEncryptedContentError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStripEncryptedReasoningContent_ClearsReasoningField(t *testing.T) {
	in := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","summary":[],"encrypted_content":"gAAAblob1"},
			{"type":"reasoning","summary":[],"encrypted_content":"gAAAblob2"}
		]
	}`)

	out, stripped := stripEncryptedReasoningContent(in)
	if !stripped {
		t.Fatal("stripped = false, want true")
	}

	for i := 1; i <= 2; i++ {
		path := "input." + strconv.Itoa(i) + ".encrypted_content"
		if gjson.GetBytes(out, path).Exists() {
			t.Errorf("%s still present", path)
		}
		if got := gjson.GetBytes(out, "input."+strconv.Itoa(i)+".type").String(); got != "reasoning" {
			t.Errorf("input.%d.type = %q after strip, want reasoning (item shape damaged)", i, got)
		}
	}

	if got := gjson.GetBytes(out, "model").String(); got != "gpt-5.5" {
		t.Errorf("model = %q after strip, want gpt-5.5 (unrelated field touched)", got)
	}

	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "hi" {
		t.Errorf("input.0.content.0.text = %q, want hi (user message damaged)", got)
	}
}

func TestStripEncryptedReasoningContent_DropsFunctionOutputEncryptedVariant(t *testing.T) {
	in := []byte(`{
		"input":[
			{"type":"function_call_output","call_id":"c1","output":[
				{"type":"input_text","text":"ok"},
				{"type":"encrypted_content","encrypted_content":"gAAAblob"},
				{"type":"input_text","text":"more"}
			]}
		]
	}`)

	out, stripped := stripEncryptedReasoningContent(in)
	if !stripped {
		t.Fatal("stripped = false, want true")
	}

	output := gjson.GetBytes(out, "input.0.output")
	if !output.IsArray() {
		t.Fatalf("input.0.output is not an array after strip: %s", output.Raw)
	}
	items := output.Array()
	if len(items) != 2 {
		t.Fatalf("output length = %d, want 2 (encrypted_content variant should have been dropped)", len(items))
	}
	for k, item := range items {
		if got := item.Get("type").String(); got == "encrypted_content" {
			t.Errorf("output[%d].type = %q after strip", k, got)
		}
	}
	if got := items[0].Get("text").String(); got != "ok" {
		t.Errorf("output[0].text = %q, want ok (input_text order disturbed)", got)
	}
	if got := items[1].Get("text").String(); got != "more" {
		t.Errorf("output[1].text = %q, want more (input_text order disturbed)", got)
	}
}

func TestStripEncryptedReasoningContent_NoEncryptedFields(t *testing.T) {
	in := []byte(`{
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","summary":[]}
		]
	}`)

	out, stripped := stripEncryptedReasoningContent(in)
	if stripped {
		t.Error("stripped = true, want false (nothing to strip)")
	}
	if string(out) != string(in) {
		t.Error("body changed despite stripped=false")
	}
}

func TestStripEncryptedReasoningContent_HandlesMissingOrInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "no input field", body: `{"model":"gpt-5"}`},
		{name: "input not an array", body: `{"input":"oops"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stripped := stripEncryptedReasoningContent([]byte(tc.body))
			if stripped {
				t.Error("stripped = true on body with nothing to strip")
			}
			if string(out) != tc.body {
				t.Errorf("body mutated: got %q, want %q", string(out), tc.body)
			}
		})
	}
}

