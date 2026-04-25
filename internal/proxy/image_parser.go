package proxy

import "encoding/json"

type ImageTokenUsage struct {
	InputTokens       int64
	OutputTokens      int64
	TotalTokens       int64
	TextInputTokens   int64
	ImageInputTokens  int64
	CachedInputTokens int64
	TextOutputTokens  int64
	ImageOutputTokens int64
}

type ImageResponseMetrics struct {
	Model        string
	Usage        ImageTokenUsage
	UsagePresent bool
}

type imageUsagePayload struct {
	InputTokens        int64                    `json:"input_tokens"`
	OutputTokens       int64                    `json:"output_tokens"`
	TotalTokens        int64                    `json:"total_tokens"`
	InputTokensDetails *imageInputTokenDetails  `json:"input_tokens_details"`
	OutputTokenDetails *imageOutputTokenDetails `json:"output_tokens_details"`
}

type imageInputTokenDetails struct {
	TextTokens   int64 `json:"text_tokens"`
	ImageTokens  int64 `json:"image_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
}

type imageOutputTokenDetails struct {
	TextTokens  int64 `json:"text_tokens"`
	ImageTokens int64 `json:"image_tokens"`
}

type imageResponseEnvelope struct {
	Model string             `json:"model"`
	Usage *imageUsagePayload `json:"usage"`
}

func ParseImageNonStreamingResponse(body []byte) (*ImageResponseMetrics, error) {
	var env imageResponseEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	m := &ImageResponseMetrics{Model: env.Model}
	if env.Usage != nil {
		m.Usage = imageUsageFromPayload(env.Usage)
		m.UsagePresent = true
	}
	return m, nil
}

type imageStreamEventEnvelope struct {
	Type  string             `json:"type"`
	Model string             `json:"model"`
	Usage *imageUsagePayload `json:"usage"`
}

func ParseImageStreamEvent(data []byte) (eventType, model string, usage ImageTokenUsage, usagePresent bool) {
	var env imageStreamEventEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", "", ImageTokenUsage{}, false
	}
	if env.Usage == nil {
		return env.Type, env.Model, ImageTokenUsage{}, false
	}
	return env.Type, env.Model, imageUsageFromPayload(env.Usage), true
}

func imageUsageFromPayload(p *imageUsagePayload) ImageTokenUsage {
	if p == nil {
		return ImageTokenUsage{}
	}
	u := ImageTokenUsage{
		InputTokens:  p.InputTokens,
		OutputTokens: p.OutputTokens,
		TotalTokens:  p.TotalTokens,
	}
	if p.InputTokensDetails != nil {
		u.TextInputTokens = p.InputTokensDetails.TextTokens
		u.ImageInputTokens = p.InputTokensDetails.ImageTokens
		u.CachedInputTokens = p.InputTokensDetails.CachedTokens
	}
	if p.OutputTokenDetails != nil {
		u.TextOutputTokens = p.OutputTokenDetails.TextTokens
		u.ImageOutputTokens = p.OutputTokenDetails.ImageTokens
	}
	if p.OutputTokens > 0 && u.TextOutputTokens == 0 && u.ImageOutputTokens == 0 {
		u.ImageOutputTokens = p.OutputTokens
	}
	return u
}
