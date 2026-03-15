package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"
)

// encodeEventStreamMessage encodes a single eventstream message to bytes.
func encodeEventStreamMessage(t *testing.T, msg eventstream.Message) []byte {
	t.Helper()
	var buf bytes.Buffer
	encoder := eventstream.NewEncoder()
	if err := encoder.Encode(&buf, msg); err != nil {
		t.Fatalf("failed to encode eventstream message: %v", err)
	}
	return buf.Bytes()
}

// makeChunkMessage creates an eventstream chunk message with the given JSON data.
func makeChunkMessage(t *testing.T, jsonData string) []byte {
	t.Helper()
	chunk := eventstreamChunk{
		Bytes: base64.StdEncoding.EncodeToString([]byte(jsonData)),
	}
	payload, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}

	msg := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.EventMessageType)},
			{Name: eventstreamapi.EventTypeHeader, Value: eventstream.StringValue("chunk")},
		},
		Payload: payload,
	}
	return encodeEventStreamMessage(t, msg)
}

func TestBedrockStreamAdapter_SingleChunk(t *testing.T) {
	sseJSON := `{"type":"message_start","message":{"id":"msg_123","model":"claude-3-sonnet","usage":{"input_tokens":10}}}`
	data := makeChunkMessage(t, sseJSON)

	adapter := newBedrockStreamAdapter(io.NopCloser(bytes.NewReader(data)))
	result, err := io.ReadAll(adapter)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}

	got := string(result)
	want := "event: message_start\ndata: " + sseJSON + "\n\n"
	if got != want {
		t.Errorf("unexpected SSE output:\n  got:  %q\n  want: %q", got, want)
	}
}

func TestBedrockStreamAdapter_MultipleChunks(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"msg_1"}}`,
		`{"type":"content_block_delta","delta":{"text":"Hello"}}`,
		`{"type":"message_delta","usage":{"output_tokens":5}}`,
	}

	var buf bytes.Buffer
	for _, evt := range events {
		buf.Write(makeChunkMessage(t, evt))
	}

	adapter := newBedrockStreamAdapter(io.NopCloser(&buf))
	result, err := io.ReadAll(adapter)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}

	got := string(result)
	expectedTypes := []string{"message_start", "content_block_delta", "message_delta"}
	for i, evt := range events {
		want := "event: " + expectedTypes[i] + "\ndata: " + evt + "\n\n"
		if !strings.Contains(got, want) {
			t.Errorf("missing expected SSE event:\n  want: %q\n  in: %q", want, got)
		}
	}
	if strings.Contains(got, "[DONE]") {
		t.Errorf("output should not contain [DONE], got:\n%s", got)
	}
}

func TestBedrockStreamAdapter_ExceptionMessage(t *testing.T) {
	errPayload, _ := json.Marshal(map[string]string{
		"Message": "throttling",
	})
	msg := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.ExceptionMessageType)},
			{Name: eventstreamapi.ExceptionTypeHeader, Value: eventstream.StringValue("ThrottlingException")},
		},
		Payload: errPayload,
	}

	data := encodeEventStreamMessage(t, msg)
	adapter := newBedrockStreamAdapter(io.NopCloser(bytes.NewReader(data)))
	_, err := io.ReadAll(adapter)
	if err == nil {
		t.Fatal("expected error from exception message")
	}
	if !strings.Contains(err.Error(), "ThrottlingException") {
		t.Errorf("error should mention ThrottlingException, got: %v", err)
	}
}

func TestBedrockStreamAdapter_ErrorMessage(t *testing.T) {
	msg := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: eventstreamapi.MessageTypeHeader, Value: eventstream.StringValue(eventstreamapi.ErrorMessageType)},
			{Name: eventstreamapi.ErrorCodeHeader, Value: eventstream.StringValue("InternalError")},
			{Name: eventstreamapi.ErrorMessageHeader, Value: eventstream.StringValue("something broke")},
		},
	}

	data := encodeEventStreamMessage(t, msg)
	adapter := newBedrockStreamAdapter(io.NopCloser(bytes.NewReader(data)))
	_, err := io.ReadAll(adapter)
	if err == nil {
		t.Fatal("expected error from error message")
	}
	if !strings.Contains(err.Error(), "InternalError") {
		t.Errorf("error should mention InternalError, got: %v", err)
	}
	if !strings.Contains(err.Error(), "something broke") {
		t.Errorf("error should mention message, got: %v", err)
	}
}

func TestBedrockStreamAdapter_MessageStopStripsMetrics(t *testing.T) {
	sseJSON := `{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":10,"outputTokenCount":20}}`
	data := makeChunkMessage(t, sseJSON)

	adapter := newBedrockStreamAdapter(io.NopCloser(bytes.NewReader(data)))
	result, err := io.ReadAll(adapter)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}

	got := string(result)
	if strings.Contains(got, "invocationMetrics") {
		t.Errorf("output should not contain invocationMetrics, got:\n%s", got)
	}
	if !strings.Contains(got, "event: message_stop\ndata: ") {
		t.Errorf("expected event: message_stop line, got:\n%s", got)
	}
	if !strings.Contains(got, `"type":"message_stop"`) {
		t.Errorf("expected type field preserved, got:\n%s", got)
	}
}

func TestBedrockStreamAdapter_EmptyStream(t *testing.T) {
	adapter := newBedrockStreamAdapter(io.NopCloser(bytes.NewReader(nil)))
	result, err := io.ReadAll(adapter)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("empty stream should produce no output, got: %q", string(result))
	}
}
