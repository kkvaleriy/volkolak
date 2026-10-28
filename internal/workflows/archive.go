package workflows

import (
	"fmt"
	"time"

	"github.com/kkvaleriy/volkolak/internal/activities/unpack"
	"github.com/kkvaleriy/volkolak/internal/fail"
	"go.temporal.io/sdk/workflow"
)

// ArchiveInput — архив, уже лежащий в объектном хранилище.
// Интервал кадров и остальные настройки воркфлоу не принимает: они задаются окружением воркера.
type ArchiveInput struct {
	Key               string `json:"key"`
	Filename          string `json:"filename"`
	ActivityTaskQueue string `json:"activity_task_queue"`
}

// ArchiveResult — что получилось по каждому видео из архива.
type ArchiveResult struct {
	ArchiveType  string        `json:"archive_type"`
	RemovedFiles int           `json:"removed_files"`
	Videos       []VideoResult `json:"videos"`
}

// ArchiveWorkflow принимает ключ архива и для каждого видео запускает VideoWorkflow.
func ArchiveWorkflow(ctx workflow.Context, input ArchiveInput) (*ArchiveResult, error) {
	logger := workflow.GetLogger(ctx)
	if input.Key == "" || input.Filename == "" || input.ActivityTaskQueue == "" {
		return nil, fail.Input("нужны key, filename и activity_task_queue")
	}

	var unpackActs *unpack.Activities

	logger.Info("1. определяю тип архива", "key", input.Key)
	var archiveType string
	err := workflow.ExecuteActivity(shortCtx(ctx, input.ActivityTaskQueue), unpackActs.DetectArchiveType, input.Key).Get(ctx, &archiveType)
	if err != nil {
		return nil, fmt.Errorf("определение типа архива: %w", err)
	}
	logger.Info("тип архива", "type", archiveType)

	runPrefix := joinKey("jobs", safeSegment(workflow.GetInfo(ctx).WorkflowExecution.ID))
	logger.Info("2. распаковываю и удаляю не-видео", "prefix", runPrefix)

	var extracted unpack.ExtractResult
	err = workflow.ExecuteActivity(
		longCtx(ctx, input.ActivityTaskQueue, time.Hour, 2*time.Hour),
		unpackActs.ExtractVideos,
		unpack.ExtractInput{
			ArchiveKey:  input.Key,
			ArchiveType: archiveType,
			RunPrefix:   runPrefix,
		},
	).Get(ctx, &extracted)
	if err != nil {
		return nil, fmt.Errorf("распаковка: %w", err)
	}
	logger.Info("видео в архиве", "count", len(extracted.Videos), "removed", extracted.Removed)

	videos := make([]VideoResult, 0, len(extracted.Videos))
	for i, video := range extracted.Videos {
		childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID:               fmt.Sprintf("%s/video-%02d", workflow.GetInfo(ctx).WorkflowExecution.ID, i),
			WorkflowExecutionTimeout: ExecutionTimeout,
		})
		var result VideoResult
		err = workflow.ExecuteChildWorkflow(childCtx, VideoWorkflow, VideoInput{
			ArchiveKey:        input.Key,
			ArchiveType:       archiveType,
			Video:             video,
			RunPrefix:         runPrefix,
			ActivityTaskQueue: input.ActivityTaskQueue,
		}).Get(ctx, &result)
		if err != nil {
			return nil, fmt.Errorf("видео %s: %w", video.RelativePath, err)
		}
		videos = append(videos, result)
	}

	return &ArchiveResult{
		ArchiveType:  archiveType,
		RemovedFiles: extracted.Removed,
		Videos:       videos,
	}, nil
}

func joinKey(parts ...string) string {
	out := ""
	for _, part := range parts {
		for len(part) > 0 && part[0] == '/' {
			part = part[1:]
		}
		for len(part) > 0 && part[len(part)-1] == '/' {
			part = part[:len(part)-1]
		}
		if part == "" {
			continue
		}
		if out != "" {
			out += "/"
		}
		out += part
	}
	return out
}
