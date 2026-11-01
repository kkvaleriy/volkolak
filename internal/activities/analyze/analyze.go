package analyze

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
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

	jpegBytes, err := fitVisionJPEG(body, 768)
	if err != nil {
		return "", err
	}
	text, err := a.chat(ctx, jpegBytes, 0.6, 1.2)
	if err == nil || !strings.Contains(err.Error(), "token repeat") {
		return text, err
	}
	actlog.Info(ctx, "модель зациклилась, повторяю на меньшем кадре")
	jpegBytes, err = fitVisionJPEG(body, 480)
	if err != nil {
		return "", err
	}
	return a.chat(ctx, jpegBytes, 0.8, 1.4)
}

func (a *Activities) chat(ctx context.Context, jpegBytes []byte, temperature, repeatPenalty float64) (string, error) {
	payload := map[string]any{
		"model":      a.Model,
		"stream":     false,
		"keep_alive": "30m",
		"options": map[string]any{
			"temperature":    temperature,
			"num_predict":    192,
			"num_ctx":        4096,
			"repeat_penalty": repeatPenalty,
			"repeat_last_n":  192,
			"top_p":          0.9,
			"top_k":          40,
		},
		"messages": []map[string]any{{
			"role":    "user",
			"content": visionPrompt,
			"images":  []string{base64.StdEncoding.EncodeToString(jpegBytes)},
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

// fitVisionJPEG уменьшает длинную сторону кадра. Иначе картинка съедает
// контекст qwen2.5vl, и модель зацикливается на одном токене.
// Не-JPEG возвращается как есть.
func fitVisionJPEG(data []byte, maxEdge int) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return data, nil
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= maxEdge && height <= maxEdge {
		return data, nil
	}
	newW, newH := width, height
	if width >= height {
		newW = maxEdge
		newH = height * maxEdge / width
	} else {
		newH = maxEdge
		newW = width * maxEdge / height
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resizeArea(img, newW, newH), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func resizeArea(src image.Image, width, height int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	for y := 0; y < height; y++ {
		y0 := y * srcH / height
		y1 := (y + 1) * srcH / height
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < width; x++ {
			x0 := x * srcW / width
			x1 := (x + 1) * srcW / width
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var red, green, blue, count int
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, b, _ := src.At(bounds.Min.X+sx, bounds.Min.Y+sy).RGBA()
					red += int(r >> 8)
					green += int(g >> 8)
					blue += int(b >> 8)
					count++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(red / count),
				G: uint8(green / count),
				B: uint8(blue / count),
				A: 255,
			})
		}
	}
	return dst
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
