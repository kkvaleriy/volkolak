package cmdx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// Exec запускает команду и возвращает stdout.
// Stderr попадает в текст ошибки, если процесс завершился неуспешно.
func Exec(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 5 * time.Second

	var stdout headWriter
	stdout.max = 2 << 20
	var stderr tailWriter
	stderr.max = 64 << 10
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	if ctx.Err() != nil {
		return stdout.String(), ctx.Err()
	}

	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("не найдена утилита %s", name),
			"ToolMissing",
			err,
		)
	}

	msg := bytes.TrimSpace(stderr.buf)
	if len(msg) == 0 {
		return stdout.String(), fmt.Errorf("%s: %w", name, err)
	}
	return stdout.String(), fmt.Errorf("%s: %w: %s", name, err, msg)
}

// Beat регулярно шлёт heartbeat со статичной деталью.
// Вне активности возвращает пустой stop.
func Beat(ctx context.Context, detail any) (stop func()) {
	return BeatDynamic(ctx, func() any { return detail })
}

// BeatDynamic шлёт heartbeat снимком, который пересчитывается на каждый тик.
// Снимок должен быть безопасен для одновременного вызова.
func BeatDynamic(ctx context.Context, snapshot func() any) (stop func()) {
	if snapshot == nil || !activity.IsActivity(ctx) {
		return func() {}
	}

	var once sync.Once
	done := make(chan struct{})
	stop = func() { once.Do(func() { close(done) }) }

	send := func() {
		activity.RecordHeartbeat(ctx, snapshot())
	}
	send()

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				send()
			}
		}
	}()
	return stop
}

type headWriter struct {
	buf []byte
	max int
}

func (w *headWriter) Write(p []byte) (int, error) {
	if w.max > 0 && len(w.buf) < w.max {
		need := w.max - len(w.buf)
		if len(p) < need {
			need = len(p)
		}
		w.buf = append(w.buf, p[:need]...)
	}
	return len(p), nil
}

func (w *headWriter) String() string { return string(w.buf) }

type tailWriter struct {
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if w.max > 0 && len(w.buf) > w.max {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}
