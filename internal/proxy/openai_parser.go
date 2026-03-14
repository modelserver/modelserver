package proxy

import (
	"encoding/json"

	"github.com/openai/openai-go/v3/responses"
)

type openaiResponseEnvelope struct {
	ID    string                  `json:"id"`
	Model string                  `json:"model"`
	Usage responses.ResponseUsage `json:"usage"`
}

// ParseOpenAINonStreamingResponse extracts model, response ID, and usage from a complete OpenAI Responses API response.
func ParseOpenAINonStreamingResponse(body []byte) (model, respID string, u responses.ResponseUsage, err error) {
	var resp openaiResponseEnvelope
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", responses.ResponseUsage{}, err
	}
	return resp.Model, resp.ID, resp.Usage, nil
}

type openaiStreamEventData struct {
	Type     string                 `json:"type"`
	Response openaiResponseEnvelope `json:"response"`
}

// ParseOpenAIStreamEvent extracts data from a single OpenAI Responses API SSE event payload.
// Returns usage for terminal events: response.completed, response.incomplete, response.failed.
func ParseOpenAIStreamEvent(data []byte) (eventType, model, respID string, u responses.ResponseUsage, hasUsage bool) {
	var evt openaiStreamEventData
	if err := json.Unmarshal(data, &evt); err != nil {
		return "", "", "", responses.ResponseUsage{}, false
	}

	eventType = evt.Type

	switch evt.Type {
	case "response.created":
		model = evt.Response.Model
		respID = evt.Response.ID
	case "response.completed", "response.incomplete", "response.failed":
		model = evt.Response.Model
		respID = evt.Response.ID
		u = evt.Response.Usage
		hasUsage = true
	}

	return eventType, model, respID, u, hasUsage
}
