//go:build integration

package kb

import (
	"context"
	"testing"
)

// Run with: go test -tags integration -run TestRealOllama ./internal/kb/
func TestRealOllama(t *testing.T) {
	o := NewOllama()
	if err := o.Check(context.Background()); err != nil {
		t.Skipf("Ollama not ready: %v", err)
	}
	vecs, err := o.Embed(context.Background(), []string{"swarm board", "kanban view"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 1024 {
		t.Fatalf("got %d vectors of %d dims", len(vecs), len(vecs[0]))
	}
}
