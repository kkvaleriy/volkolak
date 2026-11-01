package bootstrap

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
)

// Database открывает пул и ждёт, пока Postgres примет запрос.
func Database(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	var last error
	for attempt := 1; attempt <= 30; attempt++ {
		pool, err := pgxpool.New(ctx, databaseURL)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				return pool, nil
			}
			pool.Close()
		}
		last = err
		if err := sleep(ctx, 2*time.Second); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("postgres: %w", last)
}

// TemporalClient подключается к фронтенду Temporal с повторами.
func TemporalClient(ctx context.Context, address string) (client.Client, error) {
	hostPort := temporalHostPort(address)
	var last error
	for attempt := 1; attempt <= 30; attempt++ {
		c, err := client.Dial(client.Options{
			HostPort: hostPort,
			ConnectionOptions: client.ConnectionOptions{
				// Иначе холодный фронтенд на Docker Desktop не укладывается в 5 секунд.
				GetSystemInfoTimeout: 30 * time.Second,
				// tcp4 не ждёт AAAA. Чистый Go-резолвер на DNS Docker.
				DialOptions: []grpc.DialOption{
					grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
						var dialer net.Dialer
						return dialer.DialContext(ctx, "tcp4", addr)
					}),
				},
			},
		})
		if err == nil {
			return c, nil
		}
		last = err
		if err := sleep(ctx, 2*time.Second); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("temporal %s: %w", address, last)
}

// temporalHostPort обходит gRPC-резолвер dns:///. На встроенном DNS Docker он
// не успевает за таймаут клиента, хотя сам сервер уже отвечает.
func temporalHostPort(address string) string {
	if strings.Contains(address, "://") {
		return address
	}
	return "passthrough:///" + address
}

// WaitFile ждёт непустой файл. Нужен для тома с моделью whisper.
func WaitFile(ctx context.Context, path string) error {
	var last error
	for attempt := 1; attempt <= 60; attempt++ {
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			return nil
		}
		last = err
		if err == nil {
			last = fmt.Errorf("файл пустой")
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("файл %s: %w", path, last)
}

// WaitHTTP ждёт, пока URL ответит кодом меньше 500.
func WaitHTTP(ctx context.Context, rawURL string) error {
	httpClient := &http.Client{Timeout: 3 * time.Second}
	var last error
	for attempt := 1; attempt <= 60; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			last = fmt.Errorf("статус %d", resp.StatusCode)
		} else {
			last = err
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s: %w", rawURL, last)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
