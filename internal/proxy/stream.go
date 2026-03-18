package proxy

import (
	"bytes"
	"io"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// streamInterceptor wraps a response body, transparently passing through
// all bytes while parsing SSE events to extract usage data and TTFT.
type streamInterceptor struct {
	inner      io.ReadCloser
	buf        bytes.Buffer
	startTime  time.Time
	model      string
	msgID      string
	usage      anthropic.Usage
	ttft       int64
	gotFirst   bool
	onComplete func(model, msgID string, usage anthropic.Usage, ttft int64)
	once       sync.Once
}

func newStreamInterceptor(inner io.ReadCloser, startTime time.Time, onComplete func(string, string, anthropic.Usage, int64)) *streamInterceptor {
	return &streamInterceptor{
		inner:      inner,
		startTime:  startTime,
		onComplete: onComplete,
	}
}

func (si *streamInterceptor) Read(p []byte) (int, error) {
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

func (si *streamInterceptor) Close() error {
	si.flushRemaining()
	si.finish()
	return si.inner.Close()
}

func (si *streamInterceptor) processLines() {
	for {
		line, err := si.buf.ReadBytes('\n')
		if err != nil {
			si.buf.Write(line)
			return
		}
		si.parseLine(line)
	}
}

func (si *streamInterceptor) flushRemaining() {
	if si.buf.Len() > 0 {
		si.parseLine(si.buf.Bytes())
		si.buf.Reset()
	}
}

func (si *streamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}

	eventType, model, msgID, usage, hasUsage := ParseStreamEvent(data)
	if model != "" {
		si.model = model
	}
	if msgID != "" {
		si.msgID = msgID
	}

	if !si.gotFirst && eventType == "content_block_delta" {
		si.gotFirst = true
		si.ttft = time.Since(si.startTime).Milliseconds()
	}

	if hasUsage {
		switch eventType {
		case "message_start":
			si.usage.InputTokens = usage.InputTokens
			si.usage.CacheCreationInputTokens = usage.CacheCreationInputTokens
			si.usage.CacheReadInputTokens = usage.CacheReadInputTokens
		case "message_delta":
			si.usage.OutputTokens = usage.OutputTokens
		}
	}
}

func (si *streamInterceptor) finish() {
	si.once.Do(func() {
		if si.onComplete != nil {
			// Always call onComplete, even if model is empty (stream interrupted
			// before message_start). Callers rely on onComplete to release
			// resources (ConnTracker, context cancel). They fall back to the
			// request-level model when metrics.Model is empty.
			si.onComplete(si.model, si.msgID, si.usage, si.ttft)
		}
	})
}
