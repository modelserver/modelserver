package proxy

import (
	"bytes"
	"io"
	"sync"
	"time"

	"github.com/openai/openai-go/v3/responses"
)

// openaiStreamInterceptor wraps a response body, transparently passing through
// all bytes while parsing SSE events to extract usage data and TTFT for the
// OpenAI Responses API.
type openaiStreamInterceptor struct {
	inner         io.ReadCloser
	buf           bytes.Buffer
	startTime     time.Time
	modelFallback string
	model         string
	respID        string
	usage         responses.ResponseUsage
	hasUsage      bool
	ttft          int64
	gotFirst      bool
	onComplete    func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64)
	once          sync.Once
}

func newOpenAIStreamInterceptor(
	inner io.ReadCloser,
	startTime time.Time,
	modelFallback string,
	onComplete func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64),
) *openaiStreamInterceptor {
	return &openaiStreamInterceptor{
		inner:         inner,
		startTime:     startTime,
		modelFallback: modelFallback,
		onComplete:    onComplete,
	}
}

func (si *openaiStreamInterceptor) Read(p []byte) (int, error) {
	n, err := si.inner.Read(p)
	if n > 0 {
		si.buf.Write(p[:n])
		si.processLines()
	}
	if err == io.EOF {
		si.flushRemaining()
		si.finish()
	}
	return n, err
}

func (si *openaiStreamInterceptor) Close() error {
	si.flushRemaining()
	si.finish()
	return si.inner.Close()
}

func (si *openaiStreamInterceptor) processLines() {
	for {
		line, err := si.buf.ReadBytes('\n')
		if err != nil {
			si.buf.Write(line)
			return
		}
		si.parseLine(line)
	}
}

func (si *openaiStreamInterceptor) flushRemaining() {
	if si.buf.Len() > 0 {
		si.parseLine(si.buf.Bytes())
		si.buf.Reset()
	}
}

func (si *openaiStreamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}

	eventType, model, respID, usage, hasUsage := ParseOpenAIStreamEvent(data)
	if model != "" {
		si.model = model
	}
	if respID != "" {
		si.respID = respID
	}

	if !si.gotFirst && eventType == "response.output_text.delta" {
		si.gotFirst = true
		si.ttft = time.Since(si.startTime).Milliseconds()
	}

	if hasUsage {
		si.usage = usage
		si.hasUsage = true
	}
}

func (si *openaiStreamInterceptor) finish() {
	si.once.Do(func() {
		if si.onComplete != nil && si.hasUsage {
			model := si.model
			if model == "" {
				model = si.modelFallback
			}

			cachedTokens := si.usage.InputTokensDetails.CachedTokens
			inputTokens := si.usage.InputTokens - cachedTokens
			if inputTokens < 0 {
				inputTokens = 0
			}

			si.onComplete(model, si.respID, inputTokens, si.usage.OutputTokens, cachedTokens, si.ttft)
		}
	})
}
