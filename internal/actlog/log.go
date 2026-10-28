package actlog

import (
	"context"

	"go.temporal.io/sdk/activity"
)

// Info пишет в лог активности. Вне активности (юнит-тесты) вызов ничего не делает:
// GetLogger паникует на обычном context.Background.
func Info(ctx context.Context, message string, keysAndValues ...any) {
	if !activity.IsActivity(ctx) {
		return
	}
	activity.GetLogger(ctx).Info(message, keysAndValues...)
}
