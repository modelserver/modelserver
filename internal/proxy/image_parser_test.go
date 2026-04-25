package proxy

import "testing"

func TestParseImageNonStreamingResponse_FullUsage(t *testing.T) {
	body := []byte(`{
		"usage":{
			"input_tokens":100,
			"output_tokens":200,
			"total_tokens":300,
			"input_tokens_details":{"text_tokens":40,"image_tokens":60,"cached_tokens":25},
			"output_tokens_details":{"text_tokens":10,"image_tokens":190}
		}
	}`)
	got, err := ParseImageNonStreamingResponse(body)
	if err != nil {
		t.Fatalf("ParseImageNonStreamingResponse: %v", err)
	}
	if !got.UsagePresent {
		t.Fatal("UsagePresent=false, want true")
	}
	u := got.Usage
	if u.InputTokens != 100 || u.OutputTokens != 200 || u.TextInputTokens != 40 ||
		u.ImageInputTokens != 60 || u.CachedInputTokens != 25 ||
		u.TextOutputTokens != 10 || u.ImageOutputTokens != 190 {
		t.Fatalf("usage mismatch: %+v", u)
	}
}

func TestParseImageNonStreamingResponse_MissingUsage(t *testing.T) {
	got, err := ParseImageNonStreamingResponse([]byte(`{"data":[]}`))
	if err != nil {
		t.Fatalf("ParseImageNonStreamingResponse: %v", err)
	}
	if got.UsagePresent {
		t.Fatal("UsagePresent=true, want false")
	}
	if got.Usage != (ImageTokenUsage{}) {
		t.Fatalf("usage = %+v, want zero", got.Usage)
	}
}

func TestParseImageNonStreamingResponse_OutputFallback(t *testing.T) {
	got, err := ParseImageNonStreamingResponse([]byte(`{
		"usage":{"input_tokens":10,"output_tokens":77,"total_tokens":87}
	}`))
	if err != nil {
		t.Fatalf("ParseImageNonStreamingResponse: %v", err)
	}
	if got.Usage.ImageOutputTokens != 77 {
		t.Fatalf("ImageOutputTokens = %d, want 77", got.Usage.ImageOutputTokens)
	}
}

func TestParseImageStreamEvent_CompletedUsage(t *testing.T) {
	eventType, _, usage, present := ParseImageStreamEvent([]byte(`{
		"type":"image_generation.completed",
		"usage":{"input_tokens":10,"output_tokens":50,"total_tokens":60}
	}`))
	if eventType != "image_generation.completed" {
		t.Fatalf("eventType = %q", eventType)
	}
	if !present {
		t.Fatal("usagePresent=false, want true")
	}
	if usage.ImageOutputTokens != 50 {
		t.Fatalf("ImageOutputTokens = %d, want 50", usage.ImageOutputTokens)
	}
}
