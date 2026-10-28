package analyze

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		Model    string `json:"model"`
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
	require.NotEmpty(t, got.Messages)
	require.NotEmpty(t, got.Messages[0].Images[0])
}
