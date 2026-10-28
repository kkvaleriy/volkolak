package blobs

import (
	"context"
	"io"
)

// Store — постоянное хранилище файлов пайплайна.
type Store interface {
	Download(ctx context.Context, key, dest string) error
	UploadFile(ctx context.Context, key, path, contentType string) error
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
}
