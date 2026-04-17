package proxy

import (
	"encoding/json"
	"net/http"
)

// writeProxyError writes an Anthropic-style error response.
func writeProxyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    httpStatusToErrorType(status),
			"message": message,
		},
	})
}

// writeChatCompletionsError writes an OpenAI-style error response.
// Used for Chat Completions endpoint handler errors so clients using OpenAI
// SDKs receive errors in the expected format.
func writeChatCompletionsError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    httpStatusToErrorType(status),
			"code":    status,
		},
	})
}

// writeGeminiError writes a Google API-style error response.
// Used for Gemini endpoint handler errors so clients receive errors in the
// format they expect: {"error": {"code": 400, "message": "...", "status": "..."}}.
func writeGeminiError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    status,
			"message": message,
			"status":  httpStatusToGRPCStatus(status),
		},
	})
}

func httpStatusToGRPCStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusRequestEntityTooLarge:
		return "INVALID_ARGUMENT"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusInternalServerError:
		return "INTERNAL"
	case http.StatusBadGateway:
		return "UNAVAILABLE"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	default:
		return "INTERNAL"
	}
}

// Ingress-style constants used by writeUnsupportedModelError to shape the
// 400 response so that whichever client SDK made the call parses it the same
// way as other errors from that endpoint.
const (
	IngressAnthropic = "anthropic"
	IngressOpenAI    = "openai"
	IngressGemini    = "gemini"
)

// writeUnsupportedModelError emits a 400 "unsupported_model" response in the
// shape of the ingress provider. The suggestions slice is carried verbatim —
// callers pass it in already ranked and capped.
func writeUnsupportedModelError(w http.ResponseWriter, ingress, rawModel string, suggestions []string, reason string) {
	message := "unsupported model: " + rawModel
	if reason == "disabled" {
		message = "model is disabled: " + rawModel
	}
	if len(suggestions) > 0 {
		message += " (did you mean: " + joinWithCommas(suggestions) + "?)"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)

	switch ingress {
	case IngressOpenAI:
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"type":        "unsupported_model",
				"code":        "unsupported_model",
				"message":     message,
				"suggestions": suggestions,
			},
		})
	case IngressGemini:
		details := []map[string]interface{}{{
			"@type":       "type.googleapis.com/google.rpc.BadRequest",
			"suggestions": suggestions,
		}}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    http.StatusBadRequest,
				"status":  "INVALID_ARGUMENT",
				"message": message,
				"details": details,
			},
		})
	default:
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":        "unsupported_model",
				"message":     message,
				"suggestions": suggestions,
			},
		})
	}
}

// joinWithCommas is a tiny local helper to avoid pulling in strings.Join for
// one call in the error path.
func joinWithCommas(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func httpStatusToErrorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusInternalServerError:
		return "api_error"
	case http.StatusBadGateway:
		return "api_error"
	case http.StatusServiceUnavailable:
		return "overloaded_error"
	case http.StatusGatewayTimeout:
		return "timeout_error"
	default:
		return "api_error"
	}
}
