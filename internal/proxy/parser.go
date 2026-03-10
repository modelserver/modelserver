package proxy

import (
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
)

type anthropicMessageResponse struct {
	ID    string          `json:"id"`
	Model string          `json:"model"`
	Usage anthropic.Usage `json:"usage"`
}

// ParseNonStreamingResponse extracts model, message ID, and usage from a complete Anthropic response.
func ParseNonStreamingResponse(body []byte) (model, msgID string, u anthropic.Usage, err error) {
	var resp anthropicMessageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", anthropic.Usage{}, err
	}
	return resp.Model, resp.ID, resp.Usage, nil
}

type streamEventData struct {
	Type    string `json:"type"`
	Message struct {
		ID    string          `json:"id,omitempty"`
		Model string          `json:"model,omitempty"`
		Usage anthropic.Usage `json:"usage,omitempty"`
	} `json:"message,omitempty"`
	Usage anthropic.Usage `json:"usage,omitempty"`
}

// ParseStreamEvent extracts data from a single SSE event payload.
func ParseStreamEvent(data []byte) (eventType, model, msgID string, u anthropic.Usage, hasUsage bool) {
	var evt streamEventData
	if err := json.Unmarshal(data, &evt); err != nil {
		return "", "", "", anthropic.Usage{}, false
	}

	eventType = evt.Type

	switch evt.Type {
	case "message_start":
		model = evt.Message.Model
		msgID = evt.Message.ID
		u = evt.Message.Usage
		hasUsage = true
	case "message_delta":
		u = evt.Usage
		hasUsage = true
	}

	return eventType, model, msgID, u, hasUsage
}
