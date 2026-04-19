package httplog

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"unicode/utf8"
)

type document struct {
	RequestHeaders  map[string][]string `json:"request_headers"`
	RequestBody     json.RawMessage     `json:"request_body"`
	ResponseHeaders map[string][]string `json:"response_headers"`
	ResponseBody    json.RawMessage     `json:"response_body"`
	ResponseStatus  int                 `json:"response_status_code"`
	Truncated       bool                `json:"truncated,omitempty"`
}

func buildDocument(rec *Record) document {
	return document{
		RequestHeaders:  sanitizeHeaders(rec.RequestHeaders),
		RequestBody:     toRawJSON(rec.RequestBody),
		ResponseHeaders: sanitizeHeaders(rec.ResponseHeaders),
		ResponseBody:    toRawJSON(rec.ResponseBody),
		ResponseStatus:  rec.ResponseStatus,
		Truncated:       rec.Truncated,
	}
}

func toRawJSON(body []byte) json.RawMessage {
	if len(body) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	if utf8.Valid(body) {
		encoded, _ := json.Marshal(string(body))
		return json.RawMessage(encoded)
	}
	encoded, _ := json.Marshal(base64.StdEncoding.EncodeToString(body))
	return json.RawMessage(encoded)
}

func sanitizeHeaders(h http.Header) map[string][]string {
	if h == nil {
		return nil
	}
	result := make(map[string][]string, len(h))
	for k, v := range h {
		switch http.CanonicalHeaderKey(k) {
		case "X-Api-Key", "Authorization":
			result[k] = []string{"[REDACTED]"}
		default:
			result[k] = v
		}
	}
	return result
}
