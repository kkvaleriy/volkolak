package workflows

import (
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ExecutionTimeout — сколько может идти один архив, включая все видео внутри.
const ExecutionTimeout = 48 * time.Hour

func shortCtx(ctx workflow.Context, taskQueue string) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           taskQueue,
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    15 * time.Second,
			MaximumAttempts:    5,
		},
	})
}

func longCtx(ctx workflow.Context, taskQueue string, startToClose, scheduleToClose time.Duration) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:              taskQueue,
		StartToCloseTimeout:    startToClose,
		ScheduleToCloseTimeout: scheduleToClose,
		HeartbeatTimeout:       2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    3,
		},
	})
}

func safeSegment(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "wf"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}
