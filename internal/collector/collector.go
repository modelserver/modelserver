package collector

import (
	"log/slog"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// BatchWriter is the interface the collector uses to write batches.
type BatchWriter interface {
	BatchCreateRequests(requests []types.Request) error
}

// Config holds collector configuration.
type Config struct {
	BatchSize     int
	FlushInterval time.Duration
	BufferSize    int
}

// Collector batches usage events and writes them asynchronously.
type Collector struct {
	config Config
	writer BatchWriter
	logger *slog.Logger
	events chan types.Request
	done   chan struct{}
}

// New creates a new Collector.
func New(cfg Config, writer BatchWriter, logger *slog.Logger) *Collector {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 10000
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Collector{
		config: cfg,
		writer: writer,
		logger: logger,
		events: make(chan types.Request, cfg.BufferSize),
		done:   make(chan struct{}),
	}
}

// Start begins the background batch writer.
func (c *Collector) Start() {
	go c.run()
}

// Stop gracefully stops the collector, flushing remaining events.
func (c *Collector) Stop() {
	close(c.events)
	<-c.done
}

// Record sends a usage event to the collector. Non-blocking; drops if buffer is full.
func (c *Collector) Record(r types.Request) {
	select {
	case c.events <- r:
	default:
		c.logger.Warn("collector buffer full, dropping event")
	}
}

func (c *Collector) run() {
	defer close(c.done)

	batch := make([]types.Request, 0, c.config.BatchSize)
	ticker := time.NewTicker(c.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-c.events:
			if !ok {
				if len(batch) > 0 {
					c.flush(batch)
				}
				return
			}
			batch = append(batch, event)
			if len(batch) >= c.config.BatchSize {
				c.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				c.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (c *Collector) flush(batch []types.Request) {
	if err := c.writer.BatchCreateRequests(batch); err != nil {
		c.logger.Error("failed to flush batch", "error", err, "batch_size", len(batch))
	} else {
		c.logger.Debug("flushed batch", "batch_size", len(batch))
	}
}
