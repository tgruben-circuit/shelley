package memory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedder(t *testing.T) {
	const modelName = "nomic-embed-text"
	wantEmbeddings := [][]float32{
		{0.1, 0.2, 0.3},
		{0.4, 0.5, 0.6},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected path /api/embed, got %s", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var req ollamaEmbedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if req.Model != modelName {
			t.Errorf("expected model %q, got %q", modelName, req.Model)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{Embeddings: wantEmbeddings})
	}))
	defer srv.Close()

	e := NewOllamaEmbedder(srv.URL, modelName)

	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != len(wantEmbeddings) {
		t.Fatalf("expected %d embeddings, got %d", len(wantEmbeddings), len(vecs))
	}
	for i := range wantEmbeddings {
		if len(vecs[i]) != len(wantEmbeddings[i]) {
			t.Fatalf("embedding %d: expected dimension %d, got %d", i, len(wantEmbeddings[i]), len(vecs[i]))
		}
		for j := range wantEmbeddings[i] {
			if vecs[i][j] != wantEmbeddings[i][j] {
				t.Fatalf("embedding %d[%d]: expected %f, got %f", i, j, wantEmbeddings[i][j], vecs[i][j])
			}
		}
	}
}

func TestOllamaEmbedderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	e := NewOllamaEmbedder(srv.URL, "nonexistent")
	_, err := e.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error for HTTP 404, got nil")
	}
}

func TestOllamaEmbedderDimension(t *testing.T) {
	e := NewOllamaEmbedder("http://localhost:11434", "test")
	if d := e.Dimension(); d != 0 {
		t.Fatalf("expected dimension 0, got %d", d)
	}
}
