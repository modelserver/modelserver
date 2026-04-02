package proxy

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// geminiStreamInterceptor wraps a response body, transparently passing through
// all bytes while parsing SSE events to extract usage data and TTFT for the
// Gemini streaming API (streamGenerateContent?alt=sse).
//
// Each SSE data event is a complete GenerateContentResponse JSON object
// containing candidates, usageMetadata, modelVersion, and responseId.
type geminiStreamInterceptor struct {
	inner      io.ReadCloser
	buf        bytes.Buffer
	startTime  time.Time
	model      string
	respID     string
	usage      geminiUsageMetadata
	ttft       int64
	gotFirst   bool
	onComplete func(StreamMetrics)
	once       sync.Once
}

func newGeminiStreamInterceptor(
	inner io.ReadCloser,
	startTime time.Time,
	onComplete func(StreamMetrics),
) *geminiStreamInterceptor {
	return &geminiStreamInterceptor{
		inner:      inner,
		startTime:  startTime,
		onComplete: onComplete,
	}
}

func (si *geminiStreamInterceptor) Read(p []byte) (int, error) {
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

func (si *geminiStreamInterceptor) Close() error {
	si.flushRemaining()
	si.finish()
	return si.inner.Close()
}

func (si *geminiStreamInterceptor) processLines() {
	for {
		line, err := si.buf.ReadBytes('\n')
		if err != nil {
			si.buf.Write(line)
			return
		}
		si.parseLine(line)
	}
}

func (si *geminiStreamInterceptor) flushRemaining() {
	if si.buf.Len() > 0 {
		si.parseLine(si.buf.Bytes())
		si.buf.Reset()
	}
}

func (si *geminiStreamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}

	model, respID, usage, hasText := ParseGeminiStreamEvent(data)
	if model != "" {
		si.model = model
	}
	if respID != "" {
		si.respID = respID
	}

	if !si.gotFirst && hasText {
		si.gotFirst = true
		si.ttft = time.Since(si.startTime).Milliseconds()
	}

	// Always update usage — the final event has the complete counts.
	si.usage = usage
}

func (si *geminiStreamInterceptor) finish() {
	si.once.Do(func() {
		if si.onComplete != nil {
			// ThoughtsTokenCount is included in OutputTokens because thinking
			// tokens are billed at output token rates by Google.
			si.onComplete(StreamMetrics{
				Model:           si.model,
				MsgID:           si.respID,
				InputTokens:     si.usage.PromptTokenCount,
				OutputTokens:    si.usage.CandidatesTokenCount + si.usage.ThoughtsTokenCount,
				CacheReadTokens: si.usage.CachedContentTokenCount,
				TTFTMs:          si.ttft,
			})
		}
	})
}
