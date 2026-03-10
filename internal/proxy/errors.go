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

func httpStatusToErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
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
