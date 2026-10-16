package usage

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
)

func TestParseGrokSessionUsage(t *testing.T) {
	raw, err := os.ReadFile("testdata/grok-usage-run1.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseGrokSessionUsage(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := GrokUsage{InputTokens: 84202, OutputTokens: 478, CachedTokens: 57216, ReasoningTokens: 380, Turns: 1, CostUSD: 0.085448}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestParseGrokSessionUsageRejectsDocWithoutSession(t *testing.T) {
	if _, err := ParseGrokSessionUsage([]byte(`{"sessionId":"x"}`)); err == nil {
		t.Fatal("a document with no session block must be an error, not zero spend")
	}
}

func TestGrokSessionUsageRunsWithGrokHome(t *testing.T) {
	raw, err := os.ReadFile("testdata/grok-usage-run1.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotEnv map[string]string
	var gotArgs []string
	run := func(_ context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
		gotEnv, gotArgs = env, append([]string{name}, args...)
		return raw, nil
	}
	u, err := GrokSessionUsage(context.Background(), run, "/swarm", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if u.InputTokens != 84202 {
		t.Fatalf("usage not parsed: %+v", u)
	}
	if !reflect.DeepEqual(gotEnv, grokhome.Env("/swarm")) {
		t.Fatalf("env %v want %v", gotEnv, grokhome.Env("/swarm"))
	}
	if want := []string{"grok", "usage", "sess-1"}; !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("argv %v want %v", gotArgs, want)
	}
}
