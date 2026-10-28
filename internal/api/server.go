package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/kkvaleriy/volkolak/internal/blobs"
)

//go:embed openapi.yaml
var openAPISpec []byte

// Starter запускает разбор уже загруженного архива.
type Starter interface {
	Start(ctx context.Context, key, filename string) (workflowID, runID string, err error)
}

// Handler отдаёт загрузку архива и Swagger UI.
type Handler struct {
	blobs    blobs.Store
	starter  Starter
	maxBytes int64
	mux      *http.ServeMux
}

func NewHandler(store blobs.Store, starter Starter, maxBytes int64) *Handler {
	h := &Handler{blobs: store, starter: starter, maxBytes: maxBytes, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", h.health)
	h.mux.HandleFunc("POST /v1/archives", h.upload)
	h.mux.HandleFunc("GET /openapi.yaml", h.spec)
	h.mux.HandleFunc("GET /swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusFound)
	})
	h.mux.HandleFunc("GET /swagger/", h.swagger)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) spec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}

func (h *Handler) swagger(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/swagger/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerPage))
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "нужен multipart с полем file")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "поле file обязательно")
		return
	}
	defer file.Close()
	if header.Size <= 0 {
		writeError(w, http.StatusBadRequest, "пустой файл")
		return
	}

	filename := safeFilename(header.Filename)
	key := path.Join("archives", uuid.NewString(), filename)
	if err := h.blobs.Put(r.Context(), key, file, header.Size, "application/octet-stream"); err != nil {
		writeError(w, http.StatusBadGateway, "не удалось сохранить архив")
		return
	}
	workflowID, runID, err := h.starter.Start(r.Context(), key, filename)
	if err != nil {
		writeError(w, http.StatusBadGateway, "не удалось запустить обработку")
		return
	}
	writeJSON(w, http.StatusAccepted, accepted{
		WorkflowID: workflowID,
		RunID:      runID,
		Key:        key,
		Filename:   filename,
	})
}

type accepted struct {
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id"`
	Key        string `json:"key"`
	Filename   string `json:"filename"`
}

func safeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "archive.bin"
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

const swaggerPage = `<!DOCTYPE html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <title>Volkolak API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({
        url: "/openapi.yaml",
        dom_id: "#swagger-ui",
        presets: [SwaggerUIBundle.presets.apis],
        layout: "BaseLayout"
      });
    };
  </script>
</body>
</html>`
