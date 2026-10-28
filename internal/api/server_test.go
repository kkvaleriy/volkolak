package api

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type memStore struct {
	key  string
	size int64
}

func (m *memStore) Download(context.Context, string, string) error { return nil }
func (m *memStore) UploadFile(context.Context, string, string, string) error {
	return nil
}
func (m *memStore) Put(_ context.Context, key string, body io.Reader, size int64, _ string) error {
	m.key = key
	n, err := io.Copy(io.Discard, body)
	m.size = n
	if size > 0 && n != size {
		return io.ErrUnexpectedEOF
	}
	return err
}

type memStart struct {
	key, filename, workflowID, runID string
}

func (m *memStart) Start(_ context.Context, key, filename string) (string, string, error) {
	m.key = key
	m.filename = filename
	return m.workflowID, m.runID, nil
}

func TestUploadStartsWorkflow(t *testing.T) {
	store := &memStore{}
	start := &memStart{workflowID: "archive-1", runID: "run-1"}
	handler := NewHandler(store, start, 1<<20)

	body := &strings.Builder{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "film.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("zip-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/archives", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got accepted
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.WorkflowID != "archive-1" || got.RunID != "run-1" || got.Filename != "film.zip" {
		t.Fatalf("%#v", got)
	}
	if store.key == "" || !strings.HasPrefix(store.key, "archives/") || !strings.HasSuffix(store.key, "/film.zip") {
		t.Fatalf("key %s", store.key)
	}
	if start.key != store.key || start.filename != "film.zip" {
		t.Fatalf("start %#v", start)
	}
}

func TestUploadRequiresFile(t *testing.T) {
	handler := NewHandler(&memStore{}, &memStart{}, 1<<20)
	req := httptest.NewRequest(http.MethodPost, "/v1/archives", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}
