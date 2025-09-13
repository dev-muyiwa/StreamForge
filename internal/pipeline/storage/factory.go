package storage

import (
	types "StreamForge/pkg"
	"fmt"
)

func NewStorage(cfg types.StorageConfig) (Storage, error) {
	switch cfg.Type {
	case "s3":
		return NewS3Storage(cfg.S3)
	case "local":
		return NewLocalStorage(cfg.Local)
	case "gcloud", "r2":
		return nil, fmt.Errorf("gcloud and r2 backends not implemented yet")
	default:
		return nil, fmt.Errorf("unsupported storage backend: %s", cfg.Type)
	}
}
