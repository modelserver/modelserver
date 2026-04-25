package proxy

import (
	"bytes"
	"io"
	"sync"
	"time"

	"github.com/modelserver/modelserver/internal/metrics"
)

const maxImageStreamEventBytes = 10 << 20

// imageStreamInterceptor passes upstream SSE bytes through unchanged while
// extracting image usage and TTFT from image_generation/image_edit events.
type imageStreamInterceptor struct {
	inner      io.ReadCloser
	lineBuf    bytes.Buffer
	eventData  bytes.Buffer
	startTime  time.Time
	model      string
	usage      ImageTokenUsage
	hasUsage   bool
	ttft       int64
	gotFirst   bool
	parseOff   bool
	onComplete func(ImageTokenUsage, bool, int64)
	once       sync.Once
}

func newImageStreamInterceptor(inner io.ReadCloser, startTime time.Time, onComplete func(ImageTokenUsage, bool, int64)) io.ReadCloser {
	return &imageStreamInterceptor{
		inner:      inner,
		startTime:  startTime,
		onComplete: onComplete,
	}
}

func (si *imageStreamInterceptor) Read(p []byte) (int, error) {
	n, err := si.inner.Read(p)
	if n > 0 && !si.parseOff {
		si.lineBuf.Write(p[:n])
		si.processLines()
	}
	if err == io.EOF {
		si.flushRemaining()
		si.finish()
	}
	return n, err
}

func (si *imageStreamInterceptor) Close() error {
	si.flushRemaining()
	si.finish()
	return si.inner.Close()
}

func (si *imageStreamInterceptor) processLines() {
	if si.parseOff {
		return
	}
	for {
		line, err := si.lineBuf.ReadBytes('\n')
		if err != nil {
			si.lineBuf.Write(line)
			return
		}
		si.parseLine(line)
		if si.parseOff {
			return
		}
	}
}

func (si *imageStreamInterceptor) flushRemaining() {
	if si.parseOff {
		return
	}
	if si.lineBuf.Len() > 0 {
		si.parseLine(si.lineBuf.Bytes())
		si.lineBuf.Reset()
	}
	if si.eventData.Len() > 0 {
		si.parseEvent(si.eventData.Bytes())
		si.eventData.Reset()
	}
}

func (si *imageStreamInterceptor) parseLine(line []byte) {
	line = bytes.TrimRight(line, "\r\n")
	if len(bytes.TrimSpace(line)) == 0 {
		if si.eventData.Len() > 0 {
			si.parseEvent(si.eventData.Bytes())
			si.eventData.Reset()
		}
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data:"))
	data = bytes.TrimPrefix(data, []byte(" "))
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return
	}
	if si.eventData.Len() > 0 {
		if si.eventData.Len()+1 > maxImageStreamEventBytes {
			si.disableParsingForOversizeEvent()
			return
		}
		si.eventData.WriteByte('\n')
	}
	if si.eventData.Len()+len(data) > maxImageStreamEventBytes {
		si.disableParsingForOversizeEvent()
		return
	}
	si.eventData.Write(data)
}

func (si *imageStreamInterceptor) disableParsingForOversizeEvent() {
	metrics.IncImageStreamEventTooLarge()
	si.parseOff = true
	si.eventData.Reset()
	si.lineBuf.Reset()
}

func (si *imageStreamInterceptor) parseEvent(data []byte) {
	eventType, model, usage, hasUsage := ParseImageStreamEvent(data)
	if model != "" {
		si.model = model
	}
	if !si.gotFirst && (eventType == "image_generation.partial_image" || eventType == "image_edit.partial_image") {
		si.gotFirst = true
		si.ttft = time.Since(si.startTime).Milliseconds()
	}
	if eventType == "image_generation.completed" || eventType == "image_edit.completed" {
		if hasUsage {
			si.usage = usage
			si.hasUsage = true
		}
	}
}

func (si *imageStreamInterceptor) finish() {
	si.once.Do(func() {
		if si.onComplete != nil {
			si.onComplete(si.usage, si.hasUsage, si.ttft)
		}
	})
}
