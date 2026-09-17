package kb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func ollamaStub(t *testing.T, models []string, embedStatus int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			var list []map[string]string
			for _, m := range models {
				list = append(list, map[string]string{"name": m})
			}
			json.NewEncoder(w).Encode(map[string]any{"models": list})
		case "/api/embed":
			var req struct {
				Model string   `json:"model"`
				Input []string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if r.Method != http.MethodPost || req.Model != "qwen3-embedding:0.6b" {
				t.Errorf("embed request = %s %+v", r.Method, req)
			}
			if embedStatus != http.StatusOK {
				http.Error(w, "model not found", embedStatus)
				return
			}
			var out [][]float32
			for range req.Input {
				out = append(out, []float32{3, 4})
			}
			if len(req.Input) == 3 {
				out = out[:2] // simulate a short answer
			}
			json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestOllamaCheck(t *testing.T) {
	ok := ollamaStub(t, []string{"llama3:8b", "qwen3-embedding:0.6b"}, 200)
	defer ok.Close()
	o := NewOllama()
	o.BaseURL = ok.URL
	if err := o.Check(context.Background()); err != nil || o.Model() != "qwen3-embedding:0.6b" {
		t.Fatalf("Check = %v", err)
	}
	missing := ollamaStub(t, []string{"llama3:8b"}, 200)
	defer missing.Close()
	o.BaseURL = missing.URL
	if err := o.Check(context.Background()); !errors.Is(err, ErrUnavailable) ||
		err.Error() != "Search unavailable: run `ollama pull qwen3-embedding:0.6b`" {
		t.Fatalf("missing model: %v", err)
	}
	o.BaseURL = "http://127.0.0.1:1" // nothing listens
	if err := o.Check(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("down: %v", err)
	}
}

func TestOllamaEmbed(t *testing.T) {
	srv := ollamaStub(t, nil, 200)
	defer srv.Close()
	o := NewOllama()
	o.BaseURL = srv.URL
	got, err := o.Embed(context.Background(), []string{"a", "b"})
	if err != nil || len(got) != 2 || got[0][0] != 3 {
		t.Fatalf("Embed = %v, %v", got, err)
	}
	if _, err := o.Embed(context.Background(), []string{"a", "b", "c"}); err == nil {
		t.Fatal("a count mismatch must fail")
	}
	bad := ollamaStub(t, nil, 404)
	defer bad.Close()
	o.BaseURL = bad.URL
	if _, err := o.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("non-200 must fail")
	}
}
