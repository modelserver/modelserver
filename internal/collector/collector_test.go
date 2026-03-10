package collector

import (
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

type mockFlusher struct {
	flushed [][]types.Request
}

func (m *mockFlusher) BatchCreateRequests(requests []types.Request) error {
	m.flushed = append(m.flushed, requests)
	return nil
}

func TestCollectorBatchFlush(t *testing.T) {
	mock := &mockFlusher{}
	c := New(Config{
		BatchSize:     3,
		FlushInterval: 100 * time.Millisecond,
		BufferSize:    100,
	}, mock, nil)
	c.Start()
	defer c.Stop()

	for i := 0; i < 3; i++ {
		c.Record(types.Request{
			ProjectID: "proj1",
			Model:     "claude-sonnet-4",
		})
	}

	time.Sleep(200 * time.Millisecond)

	if len(mock.flushed) == 0 {
		t.Fatal("expected at least one batch flush")
	}
	if len(mock.flushed[0]) != 3 {
		t.Errorf("batch size = %d, want 3", len(mock.flushed[0]))
	}
}

func TestCollectorTimerFlush(t *testing.T) {
	mock := &mockFlusher{}
	c := New(Config{
		BatchSize:     100,
		FlushInterval: 50 * time.Millisecond,
		BufferSize:    100,
	}, mock, nil)
	c.Start()
	defer c.Stop()

	c.Record(types.Request{ProjectID: "proj1"})

	time.Sleep(150 * time.Millisecond)

	if len(mock.flushed) == 0 {
		t.Fatal("expected timer-triggered flush")
	}
	if len(mock.flushed[0]) != 1 {
		t.Errorf("batch size = %d, want 1", len(mock.flushed[0]))
	}
}
