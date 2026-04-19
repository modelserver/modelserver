package httplog

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Retrieve downloads a http log document from S3 by its key.
func (l *Logger) Retrieve(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := l.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(l.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("httplog: get object %s: %w", key, err)
	}
	defer out.Body.Close()

	return io.ReadAll(out.Body)
}
