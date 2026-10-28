package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/kkvaleriy/volkolak/internal/activities/analyze"
	"github.com/kkvaleriy/volkolak/internal/activities/media"
	"github.com/kkvaleriy/volkolak/internal/activities/store"
	"github.com/kkvaleriy/volkolak/internal/activities/transcribe"
	"github.com/kkvaleriy/volkolak/internal/activities/unpack"
	"github.com/kkvaleriy/volkolak/internal/bootstrap"
	"github.com/kkvaleriy/volkolak/internal/config"
	"github.com/kkvaleriy/volkolak/internal/s3store"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {
	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	healthURL, err := s3store.HealthURL(cfg)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("жду хранилище %s", healthURL)
	if err := bootstrap.WaitHTTP(ctx, healthURL); err != nil {
		log.Fatal(err)
	}
	log.Printf("подключение к s3 %s, бакет %s", cfg.S3Endpoint, cfg.S3Bucket)
	objects, err := s3store.Open(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("подключение к temporal %s", cfg.TemporalAddress)
	temporalClient, err := bootstrap.TemporalClient(ctx, cfg.TemporalAddress)
	if err != nil {
		log.Fatal(err)
	}
	defer temporalClient.Close()

	w, queue, err := newWorker(ctx, cfg, temporalClient, objects)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("роль %s слушает очередь %s", cfg.WorkerRole, queue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}

func newWorker(ctx context.Context, cfg config.Config, temporalClient client.Client, objects *s3store.Client) (worker.Worker, string, error) {
	switch cfg.WorkerRole {
	case "scene":
		log.Printf("жду ollama %s", cfg.OllamaURL)
		if err := bootstrap.WaitHTTP(ctx, cfg.OllamaURL); err != nil {
			return nil, "", err
		}
		w := worker.New(temporalClient, cfg.SceneTaskQueue, worker.Options{})
		w.RegisterActivity((&analyze.Activities{
			OllamaURL: cfg.OllamaURL,
			Model:     cfg.VisionModel,
			HTTP:      &http.Client{Timeout: 10 * time.Minute},
			Blobs:     objects,
		}).DescribeScenes)
		return w, cfg.SceneTaskQueue, nil
	case "transcribe":
		log.Printf("жду модель whisper %s", cfg.WhisperModel)
		if err := bootstrap.WaitFile(ctx, cfg.WhisperModel); err != nil {
			return nil, "", err
		}
		w := worker.New(temporalClient, cfg.TranscribeTaskQueue, worker.Options{})
		w.RegisterActivity((&transcribe.Activities{
			Bin:     cfg.WhisperBin,
			Model:   cfg.WhisperModel,
			Threads: cfg.WhisperThreads,
			Blobs:   objects,
		}).Transcribe)
		return w, cfg.TranscribeTaskQueue, nil
	default:
		log.Printf("подключение к postgres")
		pool, err := bootstrap.Database(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, "", err
		}
		if err := store.Migrate(ctx, pool); err != nil {
			return nil, "", err
		}
		w := worker.New(temporalClient, cfg.ActivityTaskQueue, worker.Options{})
		w.RegisterActivity(&unpack.Activities{Blobs: objects})
		w.RegisterActivity(&media.Activities{
			FFmpeg:      cfg.FFmpeg,
			FFprobe:     cfg.FFprobe,
			IntervalSec: cfg.FrameIntervalSec,
			MaxFrames:   cfg.MaxFrames,
			Blobs:       objects,
		})
		w.RegisterActivity((&analyze.Activities{
			Tesseract: cfg.Tesseract,
			Lang:      cfg.TesseractLang,
			Blobs:     objects,
		}).RecognizeText)
		w.RegisterActivity(&store.Activities{Pool: pool})
		return w, cfg.ActivityTaskQueue, nil
	}
}
