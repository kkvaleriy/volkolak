package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kkvaleriy/volkolak/internal/fail"
	"github.com/kkvaleriy/volkolak/internal/pipeline"
)

// Activities хранит имя файла, разбор кадров и расшифровку.
// Повторное сохранение того же пути возвращает уже выданный uuid.
type Activities struct {
	Pool *pgxpool.Pool
}

// SaveVideoInput — запись до тяжёлой обработки, чтобы uuid уже можно было связать.
type SaveVideoInput struct {
	OriginalFilename string `json:"original_filename"`
	RelativePath     string `json:"relative_path"`
	ObjectKey        string `json:"object_key"`
	ArchiveKey       string `json:"archive_key"`
	ArchiveType      string `json:"archive_type"`
}

// SaveVideoResult — идентификатор для будущей привязки.
type SaveVideoResult struct {
	VideoID string `json:"video_id"`
}

// SaveFramesInput — результаты шага анализа.
type SaveFramesInput struct {
	VideoID string                   `json:"video_id"`
	Frames  []pipeline.FrameAnalysis `json:"frames"`
}

// SaveTranscriptInput — результаты транскрибации.
type SaveTranscriptInput struct {
	VideoID    string              `json:"video_id"`
	Transcript pipeline.Transcript `json:"transcript"`
}

// Migrate создаёт таблицы приложения. Схему Temporal этот код не трогает.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	var legacy bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'videos' AND column_name = 'source_path'
		)`).Scan(&legacy); err != nil {
		return fmt.Errorf("проверка схемы: %w", err)
	}
	if legacy {
		for _, statement := range []string{
			`DROP TABLE IF EXISTS transcripts`,
			`DROP TABLE IF EXISTS frame_analyses`,
			`DROP TABLE IF EXISTS videos`,
		} {
			if _, err := pool.Exec(ctx, statement); err != nil {
				return fmt.Errorf("сброс старой схемы: %w", err)
			}
		}
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS videos (
			id UUID PRIMARY KEY,
			original_filename TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			object_key TEXT NOT NULL UNIQUE,
			archive_key TEXT NOT NULL,
			archive_type TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS frame_analyses (
			video_id UUID NOT NULL REFERENCES videos (id) ON DELETE CASCADE,
			frame_index INT NOT NULL,
			timestamp_sec DOUBLE PRECISION NOT NULL,
			object_key TEXT NOT NULL,
			ocr_text TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (video_id, frame_index)
		)`,
		`CREATE TABLE IF NOT EXISTS transcripts (
			video_id UUID PRIMARY KEY REFERENCES videos (id) ON DELETE CASCADE,
			language TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '',
			segments JSONB NOT NULL DEFAULT '[]'::jsonb,
			has_audio BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("миграция: %w", err)
		}
	}
	return nil
}

// SaveVideo записывает имя файла и выдаёт uuid.
func (a *Activities) SaveVideo(ctx context.Context, in SaveVideoInput) (SaveVideoResult, error) {
	if a == nil || a.Pool == nil {
		return SaveVideoResult{}, fmt.Errorf("база не настроена")
	}
	if in.OriginalFilename == "" || in.ObjectKey == "" || in.ArchiveKey == "" {
		return SaveVideoResult{}, fail.Input("не хватает имени файла или ключа объекта")
	}
	id := uuid.NewString()
	var stored string
	err := a.Pool.QueryRow(ctx, `
		INSERT INTO videos (
			id, original_filename, relative_path, object_key, archive_key, archive_type, status
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, 'registered')
		ON CONFLICT (object_key) DO UPDATE SET updated_at = now()
		RETURNING id::text
	`, id, in.OriginalFilename, in.RelativePath, in.ObjectKey, in.ArchiveKey, in.ArchiveType).Scan(&stored)
	if err != nil {
		return SaveVideoResult{}, fmt.Errorf("сохранение видео: %w", err)
	}
	return SaveVideoResult{VideoID: stored}, nil
}

// SaveFrameResults заменяет разбор кадров этого видео и отмечает статус.
func (a *Activities) SaveFrameResults(ctx context.Context, in SaveFramesInput) error {
	if a == nil || a.Pool == nil {
		return fmt.Errorf("база не настроена")
	}
	if in.VideoID == "" {
		return fail.Input("не задан video_id")
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM frame_analyses WHERE video_id = $1::uuid`, in.VideoID); err != nil {
		return fmt.Errorf("очистка кадров: %w", err)
	}
	for _, frame := range in.Frames {
		if _, err := tx.Exec(ctx, `
			INSERT INTO frame_analyses (
				video_id, frame_index, timestamp_sec, object_key, ocr_text, description, error
			) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		`, in.VideoID, frame.Index, frame.TimestampSec, frame.Key, frame.OCRText, frame.Description, frame.Error); err != nil {
			return fmt.Errorf("кадр %d: %w", frame.Index, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE videos SET status = 'analyzed', updated_at = now() WHERE id = $1::uuid
	`, in.VideoID); err != nil {
		return fmt.Errorf("статус видео: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

// SaveTranscript сохраняет расшифровку. Полный текст лежит здесь, а не только в истории Temporal.
func (a *Activities) SaveTranscript(ctx context.Context, in SaveTranscriptInput) error {
	if a == nil || a.Pool == nil {
		return fmt.Errorf("база не настроена")
	}
	if in.VideoID == "" {
		return fail.Input("не задан video_id")
	}
	segments := in.Transcript.Segments
	if segments == nil {
		segments = []pipeline.Segment{}
	}
	payload, err := json.Marshal(segments)
	if err != nil {
		return err
	}

	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO transcripts (video_id, language, text, segments, has_audio)
		VALUES ($1::uuid, $2, $3, $4::jsonb, $5)
		ON CONFLICT (video_id) DO UPDATE SET
			language = EXCLUDED.language,
			text = EXCLUDED.text,
			segments = EXCLUDED.segments,
			has_audio = EXCLUDED.has_audio,
			updated_at = now()
	`, in.VideoID, in.Transcript.Language, in.Transcript.Text, string(payload), in.Transcript.HasAudio); err != nil {
		return fmt.Errorf("сохранение расшифровки: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE videos SET status = 'done', updated_at = now() WHERE id = $1::uuid
	`, in.VideoID); err != nil {
		return fmt.Errorf("статус видео: %w", err)
	}
	return tx.Commit(ctx)
}
