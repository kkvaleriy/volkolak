package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kkvaleriy/volkolak/internal/api"
	"github.com/kkvaleriy/volkolak/internal/bootstrap"
	"github.com/kkvaleriy/volkolak/internal/config"
	"github.com/kkvaleriy/volkolak/internal/s3store"
	"github.com/kkvaleriy/volkolak/internal/workflows"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {
	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	healthURL, err := s3store.HealthURL(cfg)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("жду хранилище %s", healthURL)
	if err := bootstrap.WaitHTTP(ctx, healthURL); err != nil {
		log.Fatal(err)
	}
	log.Printf("подключение к s3 %s, бакет %s", cfg.S3Endpoint, cfg.S3Bucket)
	store, err := s3store.Open(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("подключение к temporal %s", cfg.TemporalAddress)
	temporalClient, err := bootstrap.TemporalClient(ctx, cfg.TemporalAddress)
	if err != nil {
		log.Fatal(err)
	}
	defer temporalClient.Close()

	workflowWorker := worker.New(temporalClient, cfg.WorkflowTaskQueue, worker.Options{})
	workflowWorker.RegisterWorkflow(workflows.ArchiveWorkflow)
	workflowWorker.RegisterWorkflow(workflows.VideoWorkflow)
	go func() {
		log.Printf("workflow-воркер слушает очередь %s", cfg.WorkflowTaskQueue)
		if err := workflowWorker.Run(worker.InterruptCh()); err != nil {
			log.Fatal(err)
		}
	}()

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewHandler(store, workflowStarter{client: temporalClient, cfg: cfg}, cfg.MaxUploadBytes),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("api слушает %s, swagger /swagger/", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

type workflowStarter struct {
	client client.Client
	cfg    config.Config
}

func (s workflowStarter) Start(ctx context.Context, key, filename string) (string, string, error) {
	id := "archive-" + objectID(key)
	run, err := s.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       id,
		TaskQueue:                s.cfg.WorkflowTaskQueue,
		WorkflowExecutionTimeout: s.cfg.WorkflowTimeout,
	}, workflows.ArchiveWorkflow, workflows.ArchiveInput{
		Key:               key,
		Filename:          filename,
		ActivityTaskQueue: s.cfg.ActivityTaskQueue,
	})
	if err != nil {
		return "", "", err
	}
	return run.GetID(), run.GetRunID(), nil
}

func objectID(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return key
}
