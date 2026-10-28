package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/kkvaleriy/volkolak/internal/actlog"
	"github.com/kkvaleriy/volkolak/internal/blobs"
	"github.com/kkvaleriy/volkolak/internal/cmdx"
	"github.com/kkvaleriy/volkolak/internal/fail"
	"github.com/kkvaleriy/volkolak/internal/pipeline"
	"github.com/kkvaleriy/volkolak/internal/scratch"
)

// Activities распознаёт речь локально через whisper.cpp.
//
// Это отдельная ASR-модель, а не та же vision-модель: речь она слышит,
// кадры — нет. Бинарник работает на CPU, без Python и без API-ключа.
// Мультиязычная ggml-small понимает русский; английская *.en — нет,
// поэтому в compose скачивается именно small.
type Activities struct {
	Bin     string
	Model   string
	Threads int
	Blobs   blobs.Store
}

// TranscribeInput — WAV в объектном хранилище.
type TranscribeInput struct {
	AudioKey string `json:"audio_key"`
	HasAudio bool   `json:"has_audio"`
}

// Transcribe пишет JSON whisper.cpp и сворачивает его в текст и сегменты.
func (a *Activities) Transcribe(ctx context.Context, in TranscribeInput) (pipeline.Transcript, error) {
	if !in.HasAudio {
		actlog.Info(ctx, "транскрибация пропущена: нет аудио")
		return pipeline.Transcript{Segments: []pipeline.Segment{}, HasAudio: false}, nil
	}
	if in.AudioKey == "" {
		return pipeline.Transcript{}, fail.Input("не задан ключ wav")
	}
	if a == nil || a.Blobs == nil {
		return pipeline.Transcript{}, fmt.Errorf("хранилище не настроено")
	}
	if a.Model == "" {
		return pipeline.Transcript{}, fail.Input("не задан путь к модели whisper")
	}
	info, err := os.Stat(a.Model)
	if err != nil || info.Size() == 0 {
		return pipeline.Transcript{}, fmt.Errorf("модель whisper недоступна (%s): %w", a.Model, err)
	}

	bin := a.Bin
	if bin == "" {
		bin = "whisper-cli"
	}
	threads := a.Threads
	if threads < 1 {
		threads = runtime.NumCPU()
	}

	stop := cmdx.Beat(ctx, "transcribe")
	defer stop()

	dir, cleanup, err := scratch.Dir()
	if err != nil {
		return pipeline.Transcript{}, err
	}
	defer cleanup()
	audioPath := filepath.Join(dir, "audio.wav")
	if err := a.Blobs.Download(ctx, in.AudioKey, audioPath); err != nil {
		return pipeline.Transcript{}, err
	}
	prefix := strings.TrimSuffix(audioPath, filepath.Ext(audioPath))
	_, err = cmdx.Exec(ctx, bin,
		"-m", a.Model,
		"-f", audioPath,
		"-l", "auto",
		"-ojf",
		"-of", prefix,
		"-t", strconv.Itoa(threads),
		"-np",
	)
	if err != nil {
		return pipeline.Transcript{}, fmt.Errorf("whisper: %w", err)
	}

	raw, err := os.ReadFile(prefix + ".json")
	if err != nil {
		return pipeline.Transcript{}, fmt.Errorf("json whisper: %w", err)
	}
	transcript, err := parseWhisper(raw)
	if err != nil {
		return pipeline.Transcript{}, err
	}
	transcript.HasAudio = true
	return transcript, nil
}

func parseWhisper(raw []byte) (pipeline.Transcript, error) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	var parsed struct {
		Result struct {
			Language string `json:"language"`
		} `json:"result"`
		Transcription []struct {
			Offsets struct {
				From int `json:"from"`
				To   int `json:"to"`
			} `json:"offsets"`
			Text string `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return pipeline.Transcript{}, fmt.Errorf("разбор json whisper: %w", err)
	}

	segments := make([]pipeline.Segment, 0, len(parsed.Transcription))
	var text strings.Builder
	for _, part := range parsed.Transcription {
		line := strings.TrimSpace(part.Text)
		if line == "" {
			continue
		}
		segments = append(segments, pipeline.Segment{
			StartMs: part.Offsets.From,
			EndMs:   part.Offsets.To,
			Text:    line,
		})
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(line)
	}

	full, _ := pipeline.Clip(text.String(), 500_000)
	return pipeline.Transcript{
		Language: strings.ToLower(strings.TrimSpace(parsed.Result.Language)),
		Text:     full,
		Segments: segments,
	}, nil
}
