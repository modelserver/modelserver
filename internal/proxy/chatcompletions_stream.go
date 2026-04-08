package proxy

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// chatCompletionsStreamInterceptor wraps a response body, transparently passing
// through all bytes while parsing SSE events to extract usage data and TTFT for
// the OpenAI Chat Completions streaming format.
//
// Each SSE data event is a chat.completion.chunk JSON object containing choices
// with delta content and optional usage metadata.
type chatCompletionsStreamInterceptor struct {
	inner      io.ReadCloser
	buf        bytes.Buffer
	startTime  time.Time
	model      string
	respID     string
	usage      chatCompletionsUsage
	hasUsage   bool
	ttft       int64
	gotFirst   bool
	onComplete func(StreamMetrics)
	once       sync.Once
}

func newChatCompletionsStreamInterceptor(
	inner io.ReadCloser,
	startTime time.Time,
	onComplete func(StreamMetrics),
) *chatCompletionsStreamInterceptor {
	return &chatCompletionsStreamInterceptor{
		inner:      inner,
		startTime:  startTime,
		onComplete: onComplete,
	}
}

func (si *chatCompletionsStreamInterceptor) Read(p []byte) (int, error) {
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

func (si *chatCompletionsStreamInterceptor) Close() error {
	si.flushRemaining()
	si.finish()
	return si.inner.Close()
}

func (si *chatCompletionsStreamInterceptor) processLines() {
	for {
		line, err := si.buf.ReadBytes('\n')
		if err != nil {
			si.buf.Write(line)
			return
		}
		si.parseLine(line)
	}
}

func (si *chatCompletionsStreamInterceptor) flushRemaining() {
	if si.buf.Len() > 0 {
		si.parseLine(si.buf.Bytes())
		si.buf.Reset()
	}
}

func (si *chatCompletionsStreamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}

	model, respID, usage, hasUsage, hasContent := ParseChatCompletionsStreamEvent(data)
	if model != "" {
		si.model = model
	}
	if respID != "" {
		si.respID = respID
	}

	if !si.gotFirst && hasContent {
		si.gotFirst = true
		si.ttft = time.Since(si.startTime).Milliseconds()
	}

	if hasUsage {
		si.usage = usage
		si.hasUsage = true
	}
}

func (si *chatCompletionsStreamInterceptor) finish() {
	si.once.Do(func() {
		if si.onComplete != nil {
			si.onComplete(StreamMetrics{
				Model:        si.model,
				MsgID:        si.respID,
				InputTokens:  si.usage.PromptTokens,
				OutputTokens: si.usage.CompletionTokens,
				TTFTMs:       si.ttft,
			})
		}
	})
}
