package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var ErrUnavailable = errors.New("Search unavailable: run `ollama pull qwen3-embedding:0.6b`")

type Embedder interface {
	Model() string
	Check(ctx context.Context) error
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type Ollama struct {
	BaseURL   string
	ModelName string
	HTTP      *http.Client
}

func NewOllama() *Ollama {
	return &Ollama{BaseURL: "http://127.0.0.1:11434", ModelName: "qwen3-embedding:0.6b", HTTP: &http.Client{Timeout: 60 * time.Second}}
}

func (o *Ollama) Model() string { return o.ModelName }

// Check reports ErrUnavailable unless Ollama answers and has the model.
func (o *Ollama) Check(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/tags", nil)
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&tags) != nil {
		return ErrUnavailable
	}
	for _, m := range tags.Models {
		if m.Name == o.ModelName {
			return nil
		}
	}
	return ErrUnavailable
}

func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": o.ModelName, "input": texts})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/embed", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama embed returned %d", resp.StatusCode)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama returned %d embeddings for %d inputs", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}
