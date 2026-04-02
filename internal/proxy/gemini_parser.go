package proxy

import "encoding/json"

// geminiUsageMetadata mirrors the usageMetadata object in Gemini API responses.
type geminiUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
}

// geminiResponseEnvelope is the top-level structure of a Gemini generateContent response.
// It combines the top-level fields with just enough of the candidates structure to
// detect text content (for TTFT calculation), avoiding a second JSON unmarshal.
type geminiResponseEnvelope struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata geminiUsageMetadata `json:"usageMetadata"`
	ModelVersion  string              `json:"modelVersion"`
	ResponseID    string              `json:"responseId"`
}

// ParseGeminiResponse extracts metrics from a non-streaming Gemini API response body.
// ThoughtsTokenCount is included in OutputTokens because thinking tokens are billed
// at output token rates by Google.
func ParseGeminiResponse(body []byte) (*ResponseMetrics, error) {
	var resp geminiResponseEnvelope
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return &ResponseMetrics{
		Model:           resp.ModelVersion,
		MsgID:           resp.ResponseID,
		InputTokens:     resp.UsageMetadata.PromptTokenCount,
		OutputTokens:    resp.UsageMetadata.CandidatesTokenCount + resp.UsageMetadata.ThoughtsTokenCount,
		CacheReadTokens: resp.UsageMetadata.CachedContentTokenCount,
	}, nil
}

// ParseGeminiStreamEvent extracts usage data from a single Gemini SSE event payload.
// Each SSE data line in a Gemini stream is a complete GenerateContentResponse.
// Returns whether this event contains text content (for TTFT calculation).
func ParseGeminiStreamEvent(data []byte) (model, respID string, usage geminiUsageMetadata, hasText bool) {
	var resp geminiResponseEnvelope
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", "", geminiUsageMetadata{}, false
	}

	model = resp.ModelVersion
	respID = resp.ResponseID
	usage = resp.UsageMetadata

	// Check if this event contains text content (for TTFT).
	for _, c := range resp.Candidates {
		for _, p := range c.Content.Parts {
			if p.Text != "" {
				hasText = true
				return
			}
		}
	}

	return
}
