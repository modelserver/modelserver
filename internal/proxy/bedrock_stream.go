package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"
)

// bedrockStreamAdapter wraps an AWS EventStream binary response body and
// presents it as standard SSE text/event-stream data that the existing
// streamInterceptor can consume.
type bedrockStreamAdapter struct {
	inner   io.ReadCloser
	decoder *eventstream.Decoder
	buf     []byte // buffered SSE bytes not yet returned to caller
	done    bool
	err     error
}

type eventstreamChunk struct {
	Bytes string `json:"bytes"`
}

func newBedrockStreamAdapter(inner io.ReadCloser) *bedrockStreamAdapter {
	return &bedrockStreamAdapter{
		inner:   inner,
		decoder: eventstream.NewDecoder(),
	}
}

func (a *bedrockStreamAdapter) Read(p []byte) (int, error) {
	for len(a.buf) == 0 {
		if a.done {
			return 0, io.EOF
		}
		if a.err != nil {
			return 0, a.err
		}
		a.decodeNext()
	}

	n := copy(p, a.buf)
	a.buf = a.buf[n:]
	return n, nil
}

func (a *bedrockStreamAdapter) Close() error {
	return a.inner.Close()
}

func (a *bedrockStreamAdapter) decodeNext() {
	msg, err := a.decoder.Decode(a.inner, nil)
	if err != nil {
		if err == io.EOF {
			a.buf = []byte("data: [DONE]\n\n")
			a.done = true
			return
		}
		a.err = fmt.Errorf("eventstream decode: %w", err)
		return
	}

	messageType := msg.Headers.Get(eventstreamapi.MessageTypeHeader)
	if messageType == nil {
		a.err = fmt.Errorf("%s event header not present", eventstreamapi.MessageTypeHeader)
		return
	}

	switch messageType.String() {
	case eventstreamapi.EventMessageType:
		a.handleEvent(msg)
	case eventstreamapi.ExceptionMessageType:
		a.handleException(msg)
	case eventstreamapi.ErrorMessageType:
		a.handleError(msg)
	default:
		a.err = fmt.Errorf("unknown message type: %s", messageType.String())
	}
}

func (a *bedrockStreamAdapter) handleEvent(msg eventstream.Message) {
	eventType := msg.Headers.Get(eventstreamapi.EventTypeHeader)
	if eventType == nil {
		a.err = fmt.Errorf("%s event header not present", eventstreamapi.EventTypeHeader)
		return
	}

	if eventType.String() != "chunk" {
		return
	}

	var chunk eventstreamChunk
	if err := json.Unmarshal(msg.Payload, &chunk); err != nil {
		a.err = fmt.Errorf("unmarshal chunk: %w", err)
		return
	}

	decoded, err := base64.StdEncoding.DecodeString(chunk.Bytes)
	if err != nil {
		a.err = fmt.Errorf("base64 decode chunk: %w", err)
		return
	}

	// Emit as SSE: "data: {json}\n\n"
	a.buf = append([]byte("data: "), decoded...)
	a.buf = append(a.buf, '\n', '\n')
}

func (a *bedrockStreamAdapter) handleException(msg eventstream.Message) {
	exceptionType := msg.Headers.Get(eventstreamapi.ExceptionTypeHeader)

	var errInfo struct {
		Code    string
		Type    string `json:"__type"`
		Message string
	}
	_ = json.Unmarshal(msg.Payload, &errInfo)

	errorCode := "UnknownError"
	if exceptionType != nil && exceptionType.String() != "" {
		errorCode = exceptionType.String()
	} else if errInfo.Code != "" {
		errorCode = errInfo.Code
	} else if errInfo.Type != "" {
		errorCode = errInfo.Type
	}

	errorMessage := errorCode
	if errInfo.Message != "" {
		errorMessage = errInfo.Message
	}

	a.err = fmt.Errorf("bedrock exception %s: %s", errorCode, errorMessage)
}

func (a *bedrockStreamAdapter) handleError(msg eventstream.Message) {
	errorCode := "UnknownError"
	errorMessage := errorCode

	if header := msg.Headers.Get(eventstreamapi.ErrorCodeHeader); header != nil {
		errorCode = header.String()
	}
	if header := msg.Headers.Get(eventstreamapi.ErrorMessageHeader); header != nil {
		errorMessage = header.String()
	}

	a.err = fmt.Errorf("bedrock error %s: %s", errorCode, errorMessage)
}
