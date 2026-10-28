package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config читается только из окружения. Входной архив в неё не входит:
// его присылают на HTTP-ручку.
type Config struct {
	TemporalAddress     string
	WorkflowTaskQueue   string
	ActivityTaskQueue   string
	SceneTaskQueue      string
	TranscribeTaskQueue string
	WorkerRole          string
	DatabaseURL         string
	FFmpeg              string
	FFprobe             string
	Tesseract           string
	TesseractLang       string
	WhisperBin          string
	WhisperModel        string
	WhisperThreads      int
	OllamaURL           string
	VisionModel         string
	S3Endpoint          string
	S3AccessKey         string
	S3SecretKey         string
	S3Bucket            string
	S3Region            string
	S3UseSSL            bool
	FrameIntervalSec    int
	MaxFrames           int
	HTTPAddr            string
	MaxUploadBytes      int64
	WorkflowTimeout     time.Duration
}

func FromEnv() Config {
	return Config{
		TemporalAddress:     getenv("TEMPORAL_ADDRESS", "127.0.0.1:7233"),
		WorkflowTaskQueue:   getenv("TEMPORAL_WORKFLOW_TASK_QUEUE", "volkolak-workflow"),
		ActivityTaskQueue:   getenv("TEMPORAL_ACTIVITY_TASK_QUEUE", "volkolak-activity"),
		SceneTaskQueue:      getenv("TEMPORAL_SCENE_TASK_QUEUE", "volkolak-scene"),
		TranscribeTaskQueue: getenv("TEMPORAL_TRANSCRIBE_TASK_QUEUE", "volkolak-transcribe"),
		WorkerRole:          getenv("WORKER_ROLE", "activities"),
		DatabaseURL:         getenv("DATABASE_URL", "postgres://temporal:temporal@127.0.0.1:5432/volkolak?sslmode=disable"),
		FFmpeg:              getenv("FFMPEG_BIN", "ffmpeg"),
		FFprobe:             getenv("FFPROBE_BIN", "ffprobe"),
		Tesseract:           getenv("TESSERACT_BIN", "tesseract"),
		TesseractLang:       getenv("TESSERACT_LANG", "rus+eng"),
		WhisperBin:          getenv("WHISPER_BIN", "whisper-cli"),
		WhisperModel:        getenv("WHISPER_MODEL", "/models/ggml-small.bin"),
		WhisperThreads:      getenvInt("WHISPER_THREADS", 4),
		OllamaURL:           getenv("OLLAMA_URL", "http://127.0.0.1:11434"),
		VisionModel:         getenv("VISION_MODEL", "qwen2.5vl:3b"),
		S3Endpoint:          getenv("S3_ENDPOINT", "127.0.0.1:9000"),
		S3AccessKey:         getenv("S3_ACCESS_KEY", "minio"),
		S3SecretKey:         getenv("S3_SECRET_KEY", "minioadmin"),
		S3Bucket:            getenv("S3_BUCKET", "volkolak"),
		S3Region:            getenv("S3_REGION", "us-east-1"),
		S3UseSSL:            getenvBool("S3_USE_SSL", false),
		FrameIntervalSec:    getenvInt("FRAME_INTERVAL_SEC", 2),
		MaxFrames:           getenvInt("MAX_FRAMES", 30),
		HTTPAddr:            getenv("HTTP_ADDR", ":8088"),
		MaxUploadBytes:      int64(getenvInt("HTTP_MAX_UPLOAD_MB", 2048)) << 20,
		WorkflowTimeout:     time.Duration(getenvInt("WORKFLOW_TIMEOUT_HOURS", 48)) * time.Hour,
	}
}

func (c Config) Validate() error {
	if c.S3Endpoint == "" || c.S3Bucket == "" || c.S3AccessKey == "" || c.S3SecretKey == "" {
		return fmt.Errorf("нужны S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY и S3_SECRET_KEY")
	}
	queues := []string{c.WorkflowTaskQueue, c.ActivityTaskQueue, c.SceneTaskQueue, c.TranscribeTaskQueue}
	seen := map[string]bool{}
	for _, queue := range queues {
		if queue == "" {
			return fmt.Errorf("нужны очереди воркфлоу, activities, сцен и транскрибации")
		}
		if seen[queue] {
			return fmt.Errorf("очереди Temporal должны отличаться, повтор: %s", queue)
		}
		seen[queue] = true
	}
	switch c.WorkerRole {
	case "activities", "scene", "transcribe":
	default:
		return fmt.Errorf("WORKER_ROLE должен быть activities, scene или transcribe")
	}
	if c.FrameIntervalSec < 1 || c.FrameIntervalSec > 3600 {
		return fmt.Errorf("FRAME_INTERVAL_SEC должен быть от 1 до 3600")
	}
	if c.MaxFrames < 1 || c.MaxFrames > 300 {
		return fmt.Errorf("MAX_FRAMES должен быть от 1 до 300")
	}
	if c.WorkflowTimeout <= 0 {
		return fmt.Errorf("WORKFLOW_TIMEOUT_HOURS должен быть больше нуля")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}

func getenvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}
