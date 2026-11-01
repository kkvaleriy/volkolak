package analyze

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatContent(t *testing.T) {
	text, err := chatContent([]byte(`{"message":{"content":" кот на диване "}}`))
	require.NoError(t, err)
	require.Equal(t, "кот на диване", text)

	_, err = chatContent([]byte(`{"error":"model not found"}`))
	require.Error(t, err)
}

func TestDescribeSendsImageToOllama(t *testing.T) {
	var got struct {
		Model   string `json:"model"`
		Stream  bool   `json:"stream"`
		Options struct {
			Temperature   float64 `json:"temperature"`
			NumPredict    int     `json:"num_predict"`
			NumCtx        int     `json:"num_ctx"`
			RepeatPenalty float64 `json:"repeat_penalty"`
		} `json:"options"`
		Messages []struct {
			Images []string `json:"images"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/chat", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": "пустая комната"},
		})
	}))
	t.Cleanup(server.Close)

	image := filepath.Join(t.TempDir(), "frame.jpg")
	require.NoError(t, os.WriteFile(image, []byte("jpeg-bytes"), 0o644))

	activities := &Activities{
		OllamaURL: server.URL,
		Model:     "qwen2.5vl:3b",
		HTTP:      server.Client(),
	}
	text, err := activities.describe(context.Background(), image)
	require.NoError(t, err)
	require.Equal(t, "пустая комната", text)
	require.Equal(t, "qwen2.5vl:3b", got.Model)
	require.True(t, got.Stream)
	require.Equal(t, 0.2, got.Options.Temperature)
	require.Equal(t, 80, got.Options.NumPredict)
	require.Equal(t, 4096, got.Options.NumCtx)
	require.Equal(t, 1.15, got.Options.RepeatPenalty)
	require.NotEmpty(t, got.Messages)
	require.NotEmpty(t, got.Messages[0].Images[0])
}

func TestDescribeRetriesTokenRepeatOnSmallerFrame(t *testing.T) {
	var temperatures []float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Options struct {
				Temperature float64 `json:"temperature"`
			} `json:"options"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		temperatures = append(temperatures, got.Options.Temperature)
		if len(temperatures) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"prediction aborted, token repeat limit reached"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": "тёмный кадр"},
		})
	}))
	t.Cleanup(server.Close)

	imagePath := filepath.Join(t.TempDir(), "frame.jpg")
	require.NoError(t, os.WriteFile(imagePath, []byte("jpeg-bytes"), 0o644))
	activities := &Activities{OllamaURL: server.URL, Model: "qwen2.5vl:3b", HTTP: server.Client()}
	text, err := activities.describe(context.Background(), imagePath)
	require.NoError(t, err)
	require.Equal(t, "тёмный кадр", text)
	require.Equal(t, []float64{0.2, 0.2}, temperatures)
}

func TestReadChatStreamKeepsTextBeforeRepeatAbort(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("{\"message\":{\"content\":\"В кадре пустая комната.\"}}\n")
	for range 5 {
		raw.WriteString("{\"message\":{\"content\":\".\"}}\n")
	}
	raw.WriteString("{\"error\":\"prediction aborted, token repeat limit reached\"}\n")

	text, err := readChatStream(strings.NewReader(raw.String()))
	require.NoError(t, err)
	require.Equal(t, "В кадре пустая комната", text)
}

func TestFitVisionJPEGShrinksLongEdge(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1600, 400))
	var raw bytes.Buffer
	require.NoError(t, jpeg.Encode(&raw, src, &jpeg.Options{Quality: 90}))

	fitted, err := fitVisionJPEG(raw.Bytes(), 768)
	require.NoError(t, err)
	got, err := jpeg.Decode(bytes.NewReader(fitted))
	require.NoError(t, err)
	require.Equal(t, 768, got.Bounds().Dx())
	require.Equal(t, 192, got.Bounds().Dy())
}
