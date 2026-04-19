package httplog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/modelserver/modelserver/internal/config"
)

// PathUpdater persists the S3 key back to the request row.
type PathUpdater interface {
	UpdateHttpLogPath(requestID, path string) error
}

// Logger manages async http log uploads to S3-compatible storage.
type Logger struct {
	client  *s3.Client
	bucket  string
	cfg     config.HttpLogConfig
	updater PathUpdater
	queue   chan *Record
	logger  *slog.Logger
	wg      sync.WaitGroup
}

// New creates a new body Logger. Returns nil if http logging is disabled.
func New(cfg config.HttpLogConfig, updater PathUpdater, logger *slog.Logger) (*Logger, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("httplog: load aws config: %w", err)
	}

	s3Opts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = cfg.PathStyle
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	bufSize := cfg.BufferSize
	if bufSize <= 0 {
		bufSize = 1000
	}

	return &Logger{
		client:  client,
		bucket:  cfg.Bucket,
		cfg:     cfg,
		updater: updater,
		queue:   make(chan *Record, bufSize),
		logger:  logger,
	}, nil
}

// Start begins background upload workers.
func (l *Logger) Start(workers int) {
	if workers <= 0 {
		workers = 4
	}
	l.wg.Add(workers)
	for range workers {
		go l.uploadWorker()
	}
}

// Stop drains the queue and waits for all workers to finish.
func (l *Logger) Stop() {
	close(l.queue)
	l.wg.Wait()
}

// Enqueue submits a http log record for async upload. Non-blocking; drops if full.
func (l *Logger) Enqueue(rec *Record) {
	select {
	case l.queue <- rec:
	default:
		l.logger.Warn("httplog queue full, dropping record", "request_id", rec.RequestID)
	}
}

// S3Key generates the S3 object key for a record.
func S3Key(rec *Record) string {
	date := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("%s/%s/%s.json", date, rec.ProjectID, rec.RequestID)
}

func (l *Logger) uploadWorker() {
	defer l.wg.Done()
	for rec := range l.queue {
		l.upload(rec)
	}
}

func (l *Logger) upload(rec *Record) {
	if rec.Streaming {
		reassembled, err := ReassembleAnthropicSSE(rec.ResponseBody)
		if err == nil {
			rec.ResponseBody = reassembled
		} else {
			l.logger.Warn("httplog: SSE reassembly failed, storing raw SSE",
				"request_id", rec.RequestID, "error", err)
		}
		rec.Streaming = false
	}

	key := S3Key(rec)
	doc := buildDocument(rec)

	data, err := json.Marshal(doc)
	if err != nil {
		l.logger.Error("httplog: marshal failed", "request_id", rec.RequestID, "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	contentType := "application/json"
	_, err = l.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(l.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	if err != nil {
		l.logger.Error("httplog: S3 upload failed", "request_id", rec.RequestID, "key", key, "error", err)
		return
	}

	if err := l.updater.UpdateHttpLogPath(rec.RequestID, key); err != nil {
		l.logger.Error("httplog: failed to update http_log_path", "request_id", rec.RequestID, "error", err)
	}

	l.logger.Debug("httplog: uploaded", "request_id", rec.RequestID, "key", key, "size", len(data))
}
