package httplog

import "net/http"

// Record holds all data to be logged for a single upstream request/response pair.
type Record struct {
	RequestID       string      `json:"request_id"`
	ProjectID       string      `json:"project_id"`
	RequestHeaders  http.Header `json:"request_headers"`
	RequestBody     []byte      `json:"request_body"`
	ResponseHeaders http.Header `json:"response_headers"`
	ResponseBody    []byte      `json:"response_body"`
	ResponseStatus  int         `json:"response_status_code"`
	Streaming       bool        `json:"-"`
	Truncated       bool        `json:"truncated,omitempty"`
}
