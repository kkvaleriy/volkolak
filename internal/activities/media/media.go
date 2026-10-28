package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"path"

	"github.com/kkvaleriy/volkolak/internal/actlog"
	"github.com/kkvaleriy/volkolak/internal/blobs"
	"github.com/kkvaleriy/volkolak/internal/cmdx"
	"github.com/kkvaleriy/volkolak/internal/fail"
	"github.com/kkvaleriy/volkolak/internal/pipeline"
	"github.com/kkvaleriy/volkolak/internal/scratch"
)

// Activities вырезает звук и кадры.
//
// ffmpeg выбран потому, что один и тот же бинарник читает практически любой
// контейнер и кодек: отдельно demux-ит аудио в PCM 16 кГц, который ждёт
// whisper.cpp, и отдельно сохраняет JPEG-кадры. Вторая утилита здесь ничего
// не добавляет.
type Activities struct {
	FFmpeg      string
	FFprobe     string
	IntervalSec int
	MaxFrames   int
	Blobs       blobs.Store
}

type ExtractAudioInput struct {
	VideoKey string `json:"video_key"`
	AudioKey string `json:"audio_key"`
}

type ExtractAudioResult struct {
	AudioKey string `json:"audio_key"`
	HasAudio bool   `json:"has_audio"`
}

type ExtractFramesInput struct {
	VideoKey     string `json:"video_key"`
	FramesPrefix string `json:"frames_prefix"`
}

type ExtractFramesResult struct {
	Frames []pipeline.Frame `json:"frames"`
}

// ExtractAudio пишет WAV 16 кГц моно. Если дорожки нет, это не ошибка:
// дальше транскрибация просто сохранит пустой результат.
func (a *Activities) ExtractAudio(ctx context.Context, in ExtractAudioInput) (ExtractAudioResult, error) {
	if in.VideoKey == "" || in.AudioKey == "" {
		return ExtractAudioResult{}, fail.Input("нужны ключ видео и ключ wav")
	}
	if a == nil || a.Blobs == nil {
		return ExtractAudioResult{}, fmt.Errorf("хранилище не настроено")
	}
	stop := cmdx.Beat(ctx, "extract audio")
	defer stop()

	dir, cleanup, err := scratch.Dir()
	if err != nil {
		return ExtractAudioResult{}, err
	}
	defer cleanup()
	videoPath := filepath.Join(dir, "video")
	audioPath := filepath.Join(dir, "audio.wav")
	if err := a.Blobs.Download(ctx, in.VideoKey, videoPath); err != nil {
		return ExtractAudioResult{}, err
	}
	hasAudio, err := a.extractAudioFile(ctx, videoPath, audioPath)
	if err != nil || !hasAudio {
		return ExtractAudioResult{HasAudio: hasAudio}, err
	}
	if err := a.Blobs.UploadFile(ctx, in.AudioKey, audioPath, "audio/wav"); err != nil {
		return ExtractAudioResult{}, err
	}
	return ExtractAudioResult{AudioKey: in.AudioKey, HasAudio: true}, nil
}

func (a *Activities) extractAudioFile(ctx context.Context, videoPath, destPath string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return false, err
	}

	stdout, err := cmdx.Exec(ctx, a.ffprobe(),
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_type",
		"-of", "csv=p=0",
		videoPath,
	)
	if err != nil {
		return false, fmt.Errorf("проверка аудиодорожки: %w", err)
	}
	if strings.TrimSpace(stdout) == "" {
		actlog.Info(ctx, "у видео нет аудиодорожки", "file", videoPath)
		return false, nil
	}

	_, err = cmdx.Exec(ctx, a.ffmpeg(),
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
		"-map", "0:a:0",
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		destPath,
	)
	if err != nil {
		return false, fmt.Errorf("извлечение звука: %w", err)
	}
	return true, nil
}

// ExtractFrames нарезает кадры по времени, а не каждый кадр потока.
// Каждый кадр потом идёт в OCR и vision-модель; поток 25 fps на минуту
// видео — это 1500 запросов. По умолчанию воркфлоу берёт кадр раз в 2 секунды
// и не больше 30 на ролик, растягивая шаг на всю длительность.
func (a *Activities) ExtractFrames(ctx context.Context, in ExtractFramesInput) (ExtractFramesResult, error) {
	if in.VideoKey == "" || in.FramesPrefix == "" {
		return ExtractFramesResult{}, fail.Input("нужны ключ видео и префикс кадров")
	}
	if a == nil || a.Blobs == nil {
		return ExtractFramesResult{}, fmt.Errorf("хранилище не настроено")
	}
	interval := a.IntervalSec
	if interval < 1 {
		interval = 2
	}
	maxFrames := a.MaxFrames
	if maxFrames < 1 {
		maxFrames = 30
	}

	stop := cmdx.Beat(ctx, "extract frames")
	defer stop()

	dir, cleanup, err := scratch.Dir()
	if err != nil {
		return ExtractFramesResult{}, err
	}
	defer cleanup()
	videoPath := filepath.Join(dir, "video")
	framesDir := filepath.Join(dir, "frames")
	if err := a.Blobs.Download(ctx, in.VideoKey, videoPath); err != nil {
		return ExtractFramesResult{}, err
	}
	var result ExtractFramesResult
	duration, err := a.probeDuration(ctx, videoPath)
	if err != nil {
		return ExtractFramesResult{}, err
	}
	if duration > 0 {
		result, err = a.extractAt(ctx, videoPath, framesDir, frameTimestamps(duration, interval, maxFrames))
	} else {
		result, err = a.extractFromStart(ctx, videoPath, framesDir, interval, maxFrames)
	}
	if err != nil {
		return ExtractFramesResult{}, err
	}
	for i := range result.Frames {
		localPath := result.Frames[i].Key
		objectKey := path.Join(in.FramesPrefix, filepath.Base(localPath))
		if err := a.Blobs.UploadFile(ctx, objectKey, localPath, "image/jpeg"); err != nil {
			return ExtractFramesResult{}, err
		}
		result.Frames[i].Key = objectKey
	}
	return result, nil
}

func (a *Activities) probeDuration(ctx context.Context, videoPath string) (float64, error) {
	stdout, err := cmdx.Exec(ctx, a.ffprobe(),
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		videoPath,
	)
	if err != nil {
		return 0, fmt.Errorf("длительность видео: %w", err)
	}
	return parseDuration(stdout), nil
}

func parseDuration(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "N/A" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (a *Activities) extractAt(ctx context.Context, videoPath, destDir string, stamps []float64) (ExtractFramesResult, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return ExtractFramesResult{}, err
	}
	var result ExtractFramesResult
	result.Frames = make([]pipeline.Frame, 0, len(stamps))
	for i, ts := range stamps {
		if err := ctx.Err(); err != nil {
			return ExtractFramesResult{}, err
		}
		target := filepath.Join(destDir, fmt.Sprintf("frame_%04d.jpg", i+1))
		_, err := cmdx.Exec(ctx, a.ffmpeg(),
			"-hide_banner", "-loglevel", "error", "-y",
			"-ss", strconv.FormatFloat(ts, 'f', 3, 64),
			"-i", videoPath,
			"-map", "0:v:0",
			"-frames:v", "1",
			"-vf", scaleFilter,
			"-q:v", "3",
			target,
		)
		if err != nil {
			return ExtractFramesResult{}, fmt.Errorf("кадр %.3f с: %w", ts, err)
		}
		result.Frames = append(result.Frames, pipeline.Frame{
			Index:        i,
			TimestampSec: ts,
			Key:          target,
		})
	}
	if len(result.Frames) == 0 {
		return ExtractFramesResult{}, fmt.Errorf("ffmpeg не сохранил ни одного кадра")
	}
	return result, nil
}

func (a *Activities) extractFromStart(ctx context.Context, videoPath, destDir string, interval, maxFrames int) (ExtractFramesResult, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return ExtractFramesResult{}, err
	}
	_, err := cmdx.Exec(ctx, a.ffmpeg(),
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", videoPath,
		"-map", "0:v:0",
		"-vf", fmt.Sprintf("fps=1/%d,%s", interval, scaleFilter),
		"-frames:v", strconv.Itoa(maxFrames),
		"-q:v", "3",
		filepath.Join(destDir, "frame_%04d.jpg"),
	)
	if err != nil {
		return ExtractFramesResult{}, fmt.Errorf("нарезка кадров: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(destDir, "frame_*.jpg"))
	if err != nil {
		return ExtractFramesResult{}, err
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return ExtractFramesResult{}, fmt.Errorf("ffmpeg не сохранил ни одного кадра")
	}

	result := ExtractFramesResult{Frames: make([]pipeline.Frame, 0, len(matches))}
	for i, framePath := range matches {
		result.Frames = append(result.Frames, pipeline.Frame{
			Index:        i,
			TimestampSec: float64(i * interval),
			Key:          framePath,
		})
	}
	return result, nil
}

func (a *Activities) ffmpeg() string {
	if a != nil && a.FFmpeg != "" {
		return a.FFmpeg
	}
	return "ffmpeg"
}

func (a *Activities) ffprobe() string {
	if a != nil && a.FFprobe != "" {
		return a.FFprobe
	}
	return "ffprobe"
}
