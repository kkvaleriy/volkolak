package fail

import "go.temporal.io/sdk/temporal"

// Input — ошибка входных данных. Повтор активности её не исправит.
func Input(message string) error {
	return temporal.NewNonRetryableApplicationError(message, "BadInput", nil)
}
