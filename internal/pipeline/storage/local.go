package storage

import (
	types "StreamForge/pkg"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type LocalStorage struct {
	rootPath string
}

func NewLocalStorage(localCfg types.LocalConfig) (*LocalStorage, error) {
	if localCfg.BasePath == "" {
		return nil, fmt.Errorf("base_path required for local storage")
	}
	if err := os.MkdirAll(localCfg.BasePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root path: %w", err)
	}
	return &LocalStorage{rootPath: localCfg.BasePath}, nil
}

func (l *LocalStorage) Upload(ctx context.Context, bucket, key string, body io.Reader) error {
	if bucket != "" {
		key = filepath.Join(bucket, key)
	}
	fullPath := filepath.Join(l.rootPath, key)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	out, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, body); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

func (l *LocalStorage) Download(ctx context.Context, bucket, key string) ([]byte, error) {
	if bucket != "" {
		key = filepath.Join(bucket, key)
	}
	fullPath := filepath.Join(l.rootPath, key)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return data, nil
}
