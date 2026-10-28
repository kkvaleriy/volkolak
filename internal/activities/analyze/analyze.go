package analyze

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"path/filepath"

	"github.com/kkvaleriy/volkolak/internal/actlog"
	"github.com/kkvaleriy/volkolak/internal/blobs"
	"github.com/kkvaleriy/volkolak/internal/cmdx"
	"github.com/kkvaleriy/volkolak/internal/fail"
	"github.com/kkvaleriy/volkolak/internal/pipeline"
	"github.com/kkvaleriy/volkolak/internal/scratch"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// Текст на кадре читает Tesseract: это OCR, он локальный и не выдумывает
// надписи. Что изображено, кодом без модели не определить — это запрос к
// локальной vision-модели в Ollama (по умолчанию qwen2.5vl:3b). Модель
// маленькая, понимает русский и не требует внешнего API.
const visionPrompt = "Опиши кадр по-русски в 2–4 предложениях: кто или что в кадре, где это происходит и что происходит. Не цитируй и не выдумывай надписи — текст с кадра снимается отдельно."

// Activities анализирует уже нарезанные кадры.
type Activities struct {
	Tesseract string
	Lang      string
	OllamaURL string
	Model     string
	HTTP      *http.Client
	Blobs     blobs.Store
}

// AnalyzeInput — кадры одного видео.
type AnalyzeInput struct {
	Frames []pipeline.Frame `json:"frames"`
}

// AnalyzeResult — OCR и описание в том же порядке.
type AnalyzeResult struct {
	Frames []pipeline.FrameAnalysis `json:"frames"`
}

// RecognizeText читает надписи на кадрах. Это activity основного воркера: Tesseract лёгкий.
func (a *Activities) RecognizeText(ctx context.Context, in AnalyzeInput) (AnalyzeResult, error) {
	return a.eachFrame(ctx, in, func(local string, item *pipeline.FrameAnalysis) error {
		text, err := a.ocr(ctx, local)
		if nonRetryable(err) {
			return err
		}
		if err != nil {
			item.Error = err.Error()
			return nil
		}
		item.OCRText = text
		return nil
	})
}

// DescribeScenes определяет, что изображено на кадрах. Activity крутится в отдельном контейнере.
func (a *Activities) DescribeScenes(ctx context.Context, in AnalyzeInput) (AnalyzeResult, error) {
	return a.eachFrame(ctx, in, func(local string, item *pipeline.FrameAnalysis) error {
		description, err := a.describe(ctx, local)
		if err != nil {
			return err
		}
		item.Description = description
		return nil
	})
}

func (a *Activities) eachFrame(ctx context.Context, in AnalyzeInput, visit func(local string, item *pipeline.FrameAnalysis) error) (AnalyzeResult, error) {
	var mu sync.Mutex
	var done AnalyzeResult

	if activity.IsActivity(ctx) && activity.HasHeartbeatDetails(ctx) {
		if err := activity.GetHeartbeatDetails(ctx, &done); err != nil {
			return AnalyzeResult{}, err
		}
	}
	if done.Frames == nil {
		done.Frames = []pipeline.FrameAnalysis{}
	}

	snapshot := func() any {
		mu.Lock()
		defer mu.Unlock()
		cp := done
		cp.Frames = append([]pipeline.FrameAnalysis(nil), done.Frames...)
		return cp
	}
	stop := cmdx.BeatDynamic(ctx, snapshot)
	defer stop()

	actlog.Info(ctx, "обрабатываю кадры", "count", len(in.Frames), "resume", len(done.Frames))
	for i := len(done.Frames); i < len(in.Frames); i++ {
		if err := ctx.Err(); err != nil {
			return AnalyzeResult{}, err
		}
		frame := in.Frames[i]
		local, cleanup, err := a.localFrame(ctx, frame.Key)
		if err != nil {
			return AnalyzeResult{}, err
		}
		item := pipeline.FrameAnalysis{
			Index:        frame.Index,
			TimestampSec: frame.TimestampSec,
			Key:          frame.Key,
		}
		err = visit(local, &item)
		cleanup()
		if err != nil {
			if activity.IsActivity(ctx) {
				activity.RecordHeartbeat(ctx, snapshot())
			}
			return AnalyzeResult{}, err
		}
		item.Error, _ = pipeline.Clip(item.Error, 2000)
		mu.Lock()
		done.Frames = append(done.Frames, item)
		mu.Unlock()
		if activity.IsActivity(ctx) {
			activity.RecordHeartbeat(ctx, snapshot())
		}
	}
	return done, nil
}

func (a *Activities) localFrame(ctx context.Context, key string) (string, func(), error) {
	if key == "" {
		return "", nil, fail.Input("не задан ключ кадра")
	}
	if a == nil || a.Blobs == nil {
		return "", nil, fmt.Errorf("хранилище не настроено")
	}
	dir, cleanup, err := scratch.Dir()
	if err != nil {
		return "", nil, err
	}
	local := filepath.Join(dir, "frame.jpg")
	if err := a.Blobs.Download(ctx, key, local); err != nil {
		cleanup()
		return "", nil, err
	}
	return local, cleanup, nil
}

func (a *Activities) ocr(ctx context.Context, imagePath string) (string, error) {
	bin := "tesseract"
	if a != nil && a.Tesseract != "" {
		bin = a.Tesseract
	}
	lang := "rus+eng"
	if a != nil && a.Lang != "" {
		lang = a.Lang
	}
	out, err := cmdx.Exec(ctx, bin, imagePath, "stdout", "-l", lang, "--psm", "3")
	if err != nil {
		return "", err
	}
	out = strings.ReplaceAll(out, "\f", "")
	out, _ = pipeline.Clip(out, 8000)
	return out, nil
}

func (a *Activities) describe(ctx context.Context, imagePath string) (string, error) {
	if a == nil || a.Model == "" || a.OllamaURL == "" {
		return "", fail.Input("не заданы OLLAMA_URL и VISION_MODEL")
	}
	body, err := os.ReadFile(imagePath)
	if err != nil {
		return "", err
	}
	if len(body) > 20<<20 {
		return "", fail.Input("кадр больше 20 МБ")
	}

	payload := map[string]any{
		"model":      a.Model,
		"stream":     false,
		"keep_alive": "30m",
		"options": map[string]any{
			"temperature": 0.1,
			"num_predict": 256,
		},
		"messages": []map[string]any{{
			"role":    "user",
			"content": visionPrompt,
			"images":  []string{base64.StdEncoding.EncodeToString(body)},
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(a.OllamaURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := pipeline.Clip(string(respBody), 500)
		return "", fmt.Errorf("ollama статус %d: %s", resp.StatusCode, snippet)
	}
	return chatContent(respBody)
}

func (a *Activities) httpClient() *http.Client {
	if a != nil && a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func chatContent(raw []byte) (string, error) {
	var parsed struct {
		Error   string `json:"error"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("ответ ollama: %w", err)
	}
	if parsed.Error != "" {
		return "", fmt.Errorf("ollama: %s", parsed.Error)
	}
	text, _ := pipeline.Clip(parsed.Message.Content, 2000)
	if text == "" {
		return "", fmt.Errorf("ollama вернул пустое описание")
	}
	return text, nil
}

func nonRetryable(err error) bool {
	if err == nil {
		return false
	}
	var app *temporal.ApplicationError
	return errors.As(err, &app) && app.NonRetryable()
}
