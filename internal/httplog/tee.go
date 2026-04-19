package httplog

import (
	"bytes"
	"io"
	"sync"
)

// TeeReadCloser wraps an io.ReadCloser, copying all bytes read into an
// internal buffer up to maxBytes. When Close() is called, the onComplete
// callback receives the captured bytes.
type TeeReadCloser struct {
	inner      io.ReadCloser
	buf        bytes.Buffer
	maxBytes   int64
	overflow   bool
	onComplete func(data []byte, truncated bool)
	once       sync.Once
}

func NewTeeReadCloser(
	inner io.ReadCloser,
	maxBytes int64,
	onComplete func(data []byte, truncated bool),
) *TeeReadCloser {
	return &TeeReadCloser{
		inner:      inner,
		maxBytes:   maxBytes,
		onComplete: onComplete,
	}
}

func (t *TeeReadCloser) Read(p []byte) (int, error) {
	n, err := t.inner.Read(p)
	if n > 0 && !t.overflow {
		remaining := t.maxBytes - int64(t.buf.Len())
		if remaining <= 0 {
			t.overflow = true
		} else {
			toWrite := int64(n)
			if toWrite > remaining {
				toWrite = remaining
				t.overflow = true
			}
			t.buf.Write(p[:toWrite])
		}
	}
	return n, err
}

func (t *TeeReadCloser) Close() error {
	err := t.inner.Close()
	t.once.Do(func() {
		if t.onComplete != nil {
			t.onComplete(t.buf.Bytes(), t.overflow)
		}
	})
	return err
}
