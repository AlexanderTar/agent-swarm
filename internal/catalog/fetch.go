package catalog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

type Fetched struct {
	Models       []CatalogModel
	DefaultModel string
	Source       string
}

// fetched builds a Fetched whose Models is never nil.
func fetched(ms []CatalogModel, def, source string) Fetched {
	if ms == nil {
		ms = []CatalogModel{}
	}
	return Fetched{Models: ms, DefaultModel: def, Source: source}
}

type Fetcher interface {
	Kind() kinds.AgentKind
	Version(ctx context.Context) (string, error)
	Fetch(ctx context.Context) (Fetched, error)
}

var versionRe = regexp.MustCompile(`\d+(\.\d+)+`)

func versionOf(ctx context.Context, run execx.Runner, bin string) (string, error) {
	out, err := run(ctx, bin, "--version")
	if err != nil {
		return "", err
	}
	v := versionRe.FindString(string(out))
	if v == "" {
		return "", fmt.Errorf("%s --version: no version in output", bin)
	}
	return v, nil
}

// DefaultFetchers wires the real commands and files for home (the user's $HOME).
func DefaultFetchers(home, user string) []Fetcher {
	return []Fetcher{
		&ClaudeFetcher{Run: execx.Run, HTTP: &http.Client{Timeout: 15 * time.Second}, BaseURL: "https://api.anthropic.com", User: user},
		&CodexFetcher{Run: execx.Run, Start: execx.Start, CacheFile: filepath.Join(home, ".codex", "models_cache.json"), Timeout: 10 * time.Second},
		&AgyFetcher{Run: execx.Run, SettingsFile: filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")},
		&CursorFetcher{Run: execx.Run},
	}
}

// ---- claude ----

type ClaudeFetcher struct {
	Run     execx.Runner
	HTTP    *http.Client
	BaseURL string
	User    string
}

func (f *ClaudeFetcher) Kind() kinds.AgentKind { return kinds.Claude }

func (f *ClaudeFetcher) Version(ctx context.Context) (string, error) {
	return versionOf(ctx, f.Run, "claude")
}

// maxClaudePages bounds paging so a server that always reports has_more can't hang a refresh.
const maxClaudePages = 50

// token reads the OAuth token from the keychain on every call; it is never stored.
func (f *ClaudeFetcher) token(ctx context.Context) (string, error) {
	out, err := f.Run(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-a", f.User, "-w")
	if err != nil {
		return "", errors.New("Couldn't read the Claude sign-in from the keychain.")
	}
	var cred struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(out, &cred) != nil || cred.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("Claude isn't signed in.")
	}
	return cred.ClaudeAiOauth.AccessToken, nil
}

func (f *ClaudeFetcher) Fetch(ctx context.Context) (Fetched, error) {
	token, err := f.token(ctx)
	if err != nil {
		return Fetched{}, err
	}
	version, _ := f.Version(ctx)
	var all []json.RawMessage
	after := ""
	for pages := 0; ; pages++ {
		if pages == maxClaudePages {
			return Fetched{}, fmt.Errorf("api.anthropic.com returned more than %d pages", maxClaudePages)
		}
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.BaseURL+"/v1/models?"+q.Encode(), nil)
		if err != nil {
			return Fetched{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", "claude-code/"+version)
		resp, err := f.HTTP.Do(req)
		if err != nil {
			return Fetched{}, errors.New("couldn't reach api.anthropic.com")
		}
		var page struct {
			Data    []json.RawMessage `json:"data"`
			HasMore bool              `json:"has_more"`
			LastID  string            `json:"last_id"`
		}
		status := resp.StatusCode
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if status != http.StatusOK {
			return Fetched{}, fmt.Errorf("api.anthropic.com returned %d", status)
		}
		if decodeErr != nil {
			return Fetched{}, fmt.Errorf("api.anthropic.com: %w", decodeErr)
		}
		all = append(all, page.Data...)
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	body, _ := json.Marshal(map[string]any{"data": all})
	models, err := ParseClaudeModels(body)
	if err != nil {
		return Fetched{}, err
	}
	return fetched(models, "", "api.anthropic.com/v1/models"), nil
}

// ---- codex ----

type CodexFetcher struct {
	Run       execx.Runner
	Start     execx.Starter
	CacheFile string
	Timeout   time.Duration
}

func (f *CodexFetcher) Kind() kinds.AgentKind { return kinds.Codex }

func (f *CodexFetcher) Version(ctx context.Context) (string, error) {
	return versionOf(ctx, f.Run, "codex")
}

func (f *CodexFetcher) Fetch(ctx context.Context) (Fetched, error) {
	models, def, err := f.appServer(ctx)
	if err == nil {
		return fetched(models, def, "codex app-server model/list"), nil
	}
	body, rerr := os.ReadFile(f.CacheFile)
	if rerr != nil {
		return Fetched{}, err
	}
	cached, cerr := ParseCodexModelsCache(body)
	if cerr != nil {
		return Fetched{}, err
	}
	return fetched(cached, "", "~/.codex/models_cache.json"), nil
}

func (f *CodexFetcher) appServer(ctx context.Context) ([]CatalogModel, string, error) {
	timeout := f.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p, err := f.Start(ctx, "codex", "-s", "read-only", "-a", "never", "app-server")
	if err != nil {
		return nil, "", err
	}
	defer p.Kill()
	go func() { <-ctx.Done(); p.Kill() }() // unblocks the reader on timeout

	enc := json.NewEncoder(p.Stdin)
	sc := bufio.NewScanner(p.Stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	await := func(id int) (json.RawMessage, error) {
		for sc.Scan() {
			var msg struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil || *msg.ID != id {
				continue // notifications and noise
			}
			if msg.Error != nil {
				return nil, fmt.Errorf("codex app-server: %s", msg.Error.Message)
			}
			return msg.Result, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("codex app-server: %w", ctx.Err())
		}
		return nil, errors.New("codex app-server closed before answering")
	}

	if err := enc.Encode(map[string]any{"id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]any{"name": "swarm", "version": "2"}}}); err != nil {
		return nil, "", err
	}
	if _, err := await(1); err != nil {
		return nil, "", err
	}
	if err := enc.Encode(map[string]any{"method": "initialized"}); err != nil {
		return nil, "", err
	}
	var all []CatalogModel
	def, cursor := "", ""
	for id := 2; ; id++ {
		params := map[string]any{"includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := enc.Encode(map[string]any{"id": id, "method": "model/list", "params": params}); err != nil {
			return nil, "", err
		}
		res, err := await(id)
		if err != nil {
			return nil, "", err
		}
		models, d, next, err := ParseCodexModelList(res)
		if err != nil {
			return nil, "", err
		}
		all = append(all, models...)
		if d != "" {
			def = d
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(all) == 0 {
		return nil, "", errNoModels
	}
	return all, def, nil
}

// ---- agy, cursor ----

type AgyFetcher struct {
	Run          execx.Runner
	SettingsFile string
}

func (f *AgyFetcher) Kind() kinds.AgentKind { return kinds.Agy }

func (f *AgyFetcher) Version(ctx context.Context) (string, error) {
	return versionOf(ctx, f.Run, "agy")
}

func (f *AgyFetcher) Fetch(ctx context.Context) (Fetched, error) {
	out, err := f.Run(ctx, "agy", "models")
	if err != nil {
		return Fetched{}, err
	}
	settings, _ := os.ReadFile(f.SettingsFile)
	models, def, err := ParseAgyModels(string(out), settings)
	return fetched(models, def, "agy models"), err
}

type CursorFetcher struct{ Run execx.Runner }

func (f *CursorFetcher) Kind() kinds.AgentKind { return kinds.Cursor }

func (f *CursorFetcher) Version(ctx context.Context) (string, error) {
	return versionOf(ctx, f.Run, "cursor-agent")
}

func (f *CursorFetcher) Fetch(ctx context.Context) (Fetched, error) {
	out, err := f.Run(ctx, "cursor-agent", "models")
	if err != nil {
		return Fetched{}, err
	}
	models, def, err := ParseCursorModels(string(out))
	return fetched(models, def, "cursor-agent models"), err
}
