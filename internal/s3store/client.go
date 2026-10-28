package s3store

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/kkvaleriy/volkolak/internal/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client кладёт и читает объекты в MinIO или другом S3.
type Client struct {
	api    *minio.Client
	bucket string
}

// Open подключается к S3 и создаёт бакет, если его ещё нет.
func Open(ctx context.Context, cfg config.Config) (*Client, error) {
	endpoint, secure, err := parseEndpoint(cfg.S3Endpoint, cfg.S3UseSSL)
	if err != nil {
		return nil, err
	}
	api, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: secure,
		Region: cfg.S3Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	client := &Client{api: api, bucket: cfg.S3Bucket}
	exists, err := api.BucketExists(ctx, cfg.S3Bucket)
	if err != nil {
		return nil, fmt.Errorf("s3 бакет: %w", err)
	}
	if !exists {
		if err := api.MakeBucket(ctx, cfg.S3Bucket, minio.MakeBucketOptions{Region: cfg.S3Region}); err != nil {
			return nil, fmt.Errorf("s3 создание бакета: %w", err)
		}
	}
	return client, nil
}

func (c *Client) Download(ctx context.Context, key, dest string) error {
	if err := c.api.FGetObject(ctx, c.bucket, key, dest, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("скачивание %s: %w", key, err)
	}
	return nil
}

func (c *Client) UploadFile(ctx context.Context, key, path, contentType string) error {
	_, err := c.api.FPutObject(ctx, c.bucket, key, path, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("загрузка %s: %w", key, err)
	}
	return nil
}

func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	_, err := c.api.PutObject(ctx, c.bucket, key, body, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("загрузка %s: %w", key, err)
	}
	return nil
}

func parseEndpoint(raw string, useSSL bool) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, fmt.Errorf("пустой S3_ENDPOINT")
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", false, fmt.Errorf("S3_ENDPOINT: %w", err)
		}
		if parsed.Host == "" {
			return "", false, fmt.Errorf("S3_ENDPOINT без хоста")
		}
		return parsed.Host, parsed.Scheme == "https", nil
	}
	if _, _, err := net.SplitHostPort(raw); err != nil && !strings.Contains(raw, ":") {
		return "", false, fmt.Errorf("S3_ENDPOINT: %w", err)
	}
	return raw, useSSL, nil
}

// HealthURL — проба MinIO. Нужна, чтобы воркер не стартовал раньше хранилища.
func HealthURL(cfg config.Config) (string, error) {
	endpoint, secure, err := parseEndpoint(cfg.S3Endpoint, cfg.S3UseSSL)
	if err != nil {
		return "", err
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	return scheme + "://" + endpoint + "/minio/health/live", nil
}
