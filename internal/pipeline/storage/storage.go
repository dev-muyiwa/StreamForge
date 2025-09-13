package storage

import (
	"context"
	"io"
)

type Storage interface {
	Upload(ctx context.Context, bucket, key string, body io.Reader) error
	Download(ctx context.Context, bucket, key string) ([]byte, error)
}
