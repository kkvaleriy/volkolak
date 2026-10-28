package unpack

import (
	"context"
	"fmt"
	"path"
	"path/filepath"

	"github.com/kkvaleriy/volkolak/internal/blobs"
	"github.com/kkvaleriy/volkolak/internal/cmdx"
	"github.com/kkvaleriy/volkolak/internal/fail"
	"github.com/kkvaleriy/volkolak/internal/pipeline"
	"github.com/kkvaleriy/volkolak/internal/scratch"
)

// Activities читает архив из объектного хранилища и складывает туда видео.
type Activities struct {
	Blobs blobs.Store
}

// ExtractInput — архив в бакете и префикс, куда сложить ролики.
type ExtractInput struct {
	ArchiveKey  string `json:"archive_key"`
	ArchiveType string `json:"archive_type"`
	RunPrefix   string `json:"run_prefix"`
}

// ExtractResult — ключи видео, которые остались после удаления остального.
type ExtractResult struct {
	Videos  []pipeline.VideoFile `json:"videos"`
	Removed int                  `json:"removed"`
}

// DetectArchiveType скачивает архив, определяет тип и удаляет временный файл.
func (a *Activities) DetectArchiveType(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", fail.Input("не задан ключ архива")
	}
	if a == nil || a.Blobs == nil {
		return "", fmt.Errorf("хранилище не настроено")
	}
	stop := cmdx.Beat(ctx, "detect archive")
	defer stop()

	dir, cleanup, err := scratch.Dir()
	if err != nil {
		return "", err
	}
	defer cleanup()

	local := filepath.Join(dir, "archive")
	if err := a.Blobs.Download(ctx, key, local); err != nil {
		return "", err
	}
	return detectFile(ctx, local)
}

// ExtractVideos скачивает архив, оставляет только видео и загружает их обратно в бакет.
func (a *Activities) ExtractVideos(ctx context.Context, in ExtractInput) (ExtractResult, error) {
	if in.ArchiveKey == "" || in.ArchiveType == "" || in.RunPrefix == "" {
		return ExtractResult{}, fail.Input("нужны ключ архива, тип и префикс")
	}
	if a == nil || a.Blobs == nil {
		return ExtractResult{}, fmt.Errorf("хранилище не настроено")
	}
	stop := cmdx.Beat(ctx, "extract archive")
	defer stop()

	dir, cleanup, err := scratch.Dir()
	if err != nil {
		return ExtractResult{}, err
	}
	defer cleanup()

	localArchive := filepath.Join(dir, "archive")
	if err := a.Blobs.Download(ctx, in.ArchiveKey, localArchive); err != nil {
		return ExtractResult{}, err
	}
	extracted, err := extractLocal(ctx, localInput{
		ArchivePath: localArchive,
		DestDir:     filepath.Join(dir, "out"),
		ArchiveType: in.ArchiveType,
	})
	if err != nil {
		return ExtractResult{}, err
	}

	videos := make([]pipeline.VideoFile, 0, len(extracted.Videos))
	for _, video := range extracted.Videos {
		key := path.Join(in.RunPrefix, "extracted", video.RelativePath)
		if err := a.Blobs.UploadFile(ctx, key, video.Path, "application/octet-stream"); err != nil {
			return ExtractResult{}, err
		}
		videos = append(videos, pipeline.VideoFile{
			Key:          key,
			RelativePath: video.RelativePath,
			Filename:     video.Filename,
		})
	}
	return ExtractResult{Videos: videos, Removed: extracted.Removed}, nil
}
