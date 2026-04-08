package proxy

import "encoding/json"

// chatCompletionsUsage mirrors the usage object in OpenAI Chat Completions responses.
type chatCompletionsUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// chatCompletionsResponse is the top-level structure of an OpenAI Chat Completions response.
type chatCompletionsResponse struct {
	ID    string               `json:"id"`
	Model string               `json:"model"`
	Usage chatCompletionsUsage `json:"usage"`
}

// ParseChatCompletionsResponse extracts metrics from a non-streaming Chat Completions response.
func ParseChatCompletionsResponse(body []byte) (*ResponseMetrics, error) {
	var resp chatCompletionsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &ResponseMetrics{
		Model:       resp.Model,
		MsgID:       resp.ID,
		InputTokens: resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}, nil
}

// chatCompletionsStreamEvent represents a single SSE chunk in Chat Completions streaming.
// Format: {"id":"chatcmpl-x","model":"...","choices":[{"delta":{"content":"..."},"finish_reason":null}],"usage":{...}}
type chatCompletionsStreamEvent struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *chatCompletionsUsage `json:"usage"`
}

// ParseChatCompletionsStreamEvent extracts data from a single Chat Completions SSE event.
// Returns model, response ID, usage (if present), and whether this event has text content.
func ParseChatCompletionsStreamEvent(data []byte) (model, respID string, usage chatCompletionsUsage, hasUsage, hasContent bool) {
	var evt chatCompletionsStreamEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return "", "", chatCompletionsUsage{}, false, false
	}

	model = evt.Model
	respID = evt.ID

	if evt.Usage != nil {
		usage = *evt.Usage
		hasUsage = true
	}

	for _, c := range evt.Choices {
		if c.Delta.Content != "" {
			hasContent = true
			break
		}
	}

	return
}
