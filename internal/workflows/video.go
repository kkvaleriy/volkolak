package workflows

import (
	"fmt"
	"time"

	"github.com/kkvaleriy/volkolak/internal/activities/analyze"
	"github.com/kkvaleriy/volkolak/internal/activities/media"
	"github.com/kkvaleriy/volkolak/internal/activities/store"
	"github.com/kkvaleriy/volkolak/internal/activities/transcribe"
	"github.com/kkvaleriy/volkolak/internal/fail"
	"github.com/kkvaleriy/volkolak/internal/pipeline"
	"go.temporal.io/sdk/workflow"
)

// VideoInput — одно видео, уже лежащее в объектном хранилище.
type VideoInput struct {
	ArchiveKey          string             `json:"archive_key"`
	ArchiveType         string             `json:"archive_type"`
	Video               pipeline.VideoFile `json:"video"`
	RunPrefix           string             `json:"run_prefix"`
	ActivityTaskQueue   string             `json:"activity_task_queue"`
	SceneTaskQueue      string             `json:"scene_task_queue"`
	TranscribeTaskQueue string             `json:"transcribe_task_queue"`
}

// VideoResult — краткий итог. Полные текст и описания лежат в базе, файлы — в бакете.
type VideoResult struct {
	VideoID             string `json:"video_id"`
	Filename            string `json:"filename"`
	RelativePath        string `json:"relative_path"`
	ObjectKey           string `json:"object_key"`
	FrameCount          int    `json:"frame_count"`
	Language            string `json:"language"`
	HasAudio            bool   `json:"has_audio"`
	Transcript          string `json:"transcript"`
	TranscriptTruncated bool   `json:"transcript_truncated"`
}

// VideoWorkflow обрабатывает один ролик: шаги 3–9.
func VideoWorkflow(ctx workflow.Context, input VideoInput) (*VideoResult, error) {
	logger := workflow.GetLogger(ctx)
	if input.Video.Key == "" || input.Video.Filename == "" || input.RunPrefix == "" || input.ArchiveKey == "" || input.ActivityTaskQueue == "" || input.SceneTaskQueue == "" || input.TranscribeTaskQueue == "" {
		return nil, fail.Input("не хватает ключа видео, имени файла, префикса или очередей")
	}
	queue := input.ActivityTaskQueue

	var storeActs *store.Activities
	var mediaActs *media.Activities
	var analyzeActs *analyze.Activities
	var speechActs *transcribe.Activities

	logger.Info("3. сохраняю имя файла", "file", input.Video.Filename)
	var saved store.SaveVideoResult
	err := workflow.ExecuteActivity(shortCtx(ctx, queue), storeActs.SaveVideo, store.SaveVideoInput{
		OriginalFilename: input.Video.Filename,
		RelativePath:     input.Video.RelativePath,
		ObjectKey:        input.Video.Key,
		ArchiveKey:       input.ArchiveKey,
		ArchiveType:      input.ArchiveType,
	}).Get(ctx, &saved)
	if err != nil {
		return nil, fmt.Errorf("сохранение файла: %w", err)
	}

	base := joinKey(input.RunPrefix, saved.VideoID)
	audioKey := joinKey(base, "audio.wav")
	framesPrefix := joinKey(base, "frames")

	logger.Info("4. извлекаю звуковую дорожку", "video_id", saved.VideoID)
	var audio media.ExtractAudioResult
	err = workflow.ExecuteActivity(
		longCtx(ctx, queue, time.Hour, 2*time.Hour),
		mediaActs.ExtractAudio,
		media.ExtractAudioInput{VideoKey: input.Video.Key, AudioKey: audioKey},
	).Get(ctx, &audio)
	if err != nil {
		return nil, fmt.Errorf("звуковая дорожка: %w", err)
	}

	logger.Info("5. нарезаю кадры")
	var frames media.ExtractFramesResult
	err = workflow.ExecuteActivity(
		longCtx(ctx, queue, time.Hour, 2*time.Hour),
		mediaActs.ExtractFrames,
		media.ExtractFramesInput{VideoKey: input.Video.Key, FramesPrefix: framesPrefix},
	).Get(ctx, &frames)
	if err != nil {
		return nil, fmt.Errorf("нарезка кадров: %w", err)
	}

	logger.Info("6. читаю текст на кадрах", "count", len(frames.Frames))
	var texts analyze.AnalyzeResult
	err = workflow.ExecuteActivity(
		longCtx(ctx, queue, 2*time.Hour, 4*time.Hour),
		analyzeActs.RecognizeText,
		analyze.AnalyzeInput{Frames: frames.Frames},
	).Get(ctx, &texts)
	if err != nil {
		return nil, fmt.Errorf("текст на кадрах: %w", err)
	}

	logger.Info("7. определяю сцены", "queue", input.SceneTaskQueue)
	var scenes analyze.AnalyzeResult
	err = workflow.ExecuteActivity(
		longCtx(ctx, input.SceneTaskQueue, 12*time.Hour, 24*time.Hour),
		analyzeActs.DescribeScenes,
		analyze.AnalyzeInput{Frames: frames.Frames},
	).Get(ctx, &scenes)
	if err != nil {
		return nil, fmt.Errorf("определение сцен: %w", err)
	}
	analysis, err := mergeFrameAnalysis(texts.Frames, scenes.Frames)
	if err != nil {
		return nil, err
	}

	logger.Info("8. сохраняю разбор кадров", "count", len(analysis))
	err = workflow.ExecuteActivity(shortCtx(ctx, queue), storeActs.SaveFrameResults, store.SaveFramesInput{
		VideoID: saved.VideoID,
		Frames:  analysis,
	}).Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("сохранение кадров: %w", err)
	}

	logger.Info("9. транскрибирую звук", "has_audio", audio.HasAudio, "queue", input.TranscribeTaskQueue)
	var transcript pipeline.Transcript
	err = workflow.ExecuteActivity(
		longCtx(ctx, input.TranscribeTaskQueue, 12*time.Hour, 24*time.Hour),
		speechActs.Transcribe,
		transcribe.TranscribeInput{AudioKey: audio.AudioKey, HasAudio: audio.HasAudio},
	).Get(ctx, &transcript)
	if err != nil {
		return nil, fmt.Errorf("транскрибация: %w", err)
	}

	logger.Info("10. сохраняю расшифровку", "language", transcript.Language)
	err = workflow.ExecuteActivity(shortCtx(ctx, queue), storeActs.SaveTranscript, store.SaveTranscriptInput{
		VideoID:    saved.VideoID,
		Transcript: transcript,
	}).Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("сохранение расшифровки: %w", err)
	}

	preview, truncated := pipeline.Clip(transcript.Text, 4000)
	return &VideoResult{
		VideoID:             saved.VideoID,
		Filename:            input.Video.Filename,
		RelativePath:        input.Video.RelativePath,
		ObjectKey:           input.Video.Key,
		FrameCount:          len(analysis),
		Language:            transcript.Language,
		HasAudio:            transcript.HasAudio,
		Transcript:          preview,
		TranscriptTruncated: truncated,
	}, nil
}

func mergeFrameAnalysis(texts, scenes []pipeline.FrameAnalysis) ([]pipeline.FrameAnalysis, error) {
	if len(texts) != len(scenes) {
		return nil, fmt.Errorf("текст и сцены разошлись: %d и %d кадров", len(texts), len(scenes))
	}
	merged := make([]pipeline.FrameAnalysis, len(texts))
	for i := range texts {
		item := texts[i]
		item.Description = scenes[i].Description
		if scenes[i].Error != "" {
			if item.Error != "" {
				item.Error += "; "
			}
			item.Error += scenes[i].Error
		}
		merged[i] = item
	}
	return merged, nil
}
