package httpapi

// No //go:build tag: this file is an ordinary test in package httpapi. The e2e
// build tag belongs to scripts/e2e/, which is a separate package.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

// contract_test.go checks the menubar's JSON fixtures against the Go wire
// types, so the two cannot drift (§23.3 row 23). It is skipped until P4's
// fixtures land on this branch, or SWARM_CONTRACT_FIXTURES points at them.
func fixturesDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("SWARM_CONTRACT_FIXTURES"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "contract.json")); err == nil {
			return dir
		}
	}
	for _, dir := range []string{
		filepath.Join("..", "..", "apps", "menubar", "Tests", "Fixtures"),
		filepath.Join("..", "..", "..", "agent-swarm--p4-menubar", "apps", "menubar", "Tests", "Fixtures"),
	} {
		if _, err := os.Stat(filepath.Join(dir, "contract.json")); err == nil {
			return dir
		}
	}
	t.Skip("P4 fixtures are not present; set SWARM_CONTRACT_FIXTURES")
	return ""
}

type contractEntry struct {
	File      string `json:"file"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Direction string `json:"direction"`
	Status    int    `json:"status"`
}

// routePattern turns a concrete path, query string included, into the
// {name}-form pattern the matching route in s.routes was registered with —
// walking the live route table rather than keeping a second copy of it, so
// the two can never drift from each other. A path with no matching route
// (the true SSE-stream case, method "") comes back unchanged.
func (s *Server) routePattern(method, path string) string {
	path, _, _ = strings.Cut(path, "?")
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for _, r := range s.routes {
		if r.method != method {
			continue
		}
		rsegs := strings.Split(strings.Trim(r.pattern, "/"), "/")
		if len(rsegs) != len(segs) {
			continue
		}
		match := true
		for i, rs := range rsegs {
			if strings.HasPrefix(rs, "{") {
				continue
			}
			if rs != segs[i] {
				match = false
				break
			}
		}
		if match {
			return r.pattern
		}
	}
	return path
}

// errorEnvelope is §7's error shape, decoded for any fixture whose status is
// an error (>= 400): those never match the route's ordinary success type
// (error-conflict.json against agentNodeWire, say), and don't need to — the
// envelope is the same for every route.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// typeFor maps one fixture entry's direction/method/route-pattern key to the
// Go type it must decode into. A fixture with no mapping is a test failure:
// the map is how a new route gets covered.
func typeFor(key string) (any, bool) {
	switch key {
	case "response GET /api/state":
		return &stateWire{}, true
	case "response POST /api/agents/{name}/pause":
		return &agentNodeWire{}, true
	case "response GET /api/notifications":
		return &[]notificationWire{}, true
	case "response GET /api/usage":
		return &[]usageWire{}, true
	// P1-owned routes: the domain structs are not the wire shape (W6). Settings
	// happens to marshal as itself, but AgentCatalogEntry has MarshalJSON only and
	// a time.Time field, so an int-ms fixture will not decode into it. Those
	// entries check key presence against the live handler's output instead
	// (GET only — see the loose-type recheck below).
	case "response GET /api/settings", "response PUT /api/settings":
		return &settings.Settings{}, true
	case "response GET /api/catalog", "response POST /api/catalog/refresh":
		return &[]map[string]any{}, true
	case "response GET /api/repos":
		return &map[string]any{}, true // ReposResponse is an object, not a list
	case "response POST /api/repos":
		return &repoWire{}, true
	case "response POST /api/repos/rescan":
		return &repos.ScanStats{}, true
	case "response GET /api/requests", "response POST /api/requests/{id}/answer":
		return &requestWire{}, true
	case "response POST /api/spikes":
		return &spikeResponseWire{}, true
	case "response POST /api/agents/{name}/terminal":
		return &terminalWire{}, true
	case "response GET /api/agents/{name}/pane":
		return &paneWire{}, true
	case "response POST /api/pause-all":
		return &pauseAllWire{}, true
	case "response POST /api/notifications/read-all":
		return &readAllWire{}, true
	case "request POST /api/spikes":
		return &spikeRequestBody{}, true
	case "request POST /api/items/{key}/orchestrator":
		return &orchestratorRequestBody{}, true
	case "request POST /api/requests/{id}/answer":
		return &answerBody{}, true
	case "request POST /api/requests/{id}/approve":
		return &approveBody{}, true
	case "request POST /api/requests/{id}/changes", "request POST /api/requests/{id}/request-changes":
		return &changesBody{}, true
	case "request POST /api/requests/{id}/confirm-repos":
		return &confirmReposBody{}, true
	case "request PUT /api/settings":
		return &settings.Settings{}, true
	case "request POST /api/agents/{name}/pause", "request POST /api/pause-all":
		return &pauseBody{}, true
	case "request POST /api/agents/{name}/terminal":
		return &terminalBody{}, true
	case "request POST /api/usage/refresh":
		return &usageRefreshBody{}, true
	case "request POST /api/repos":
		// addRepo's body is an unexported inline struct (config.go); mirrored
		// here rather than exported just for this test.
		return &struct {
			Path string `json:"path"`
		}{}, true
	case "request POST /api/notifications/{id}/read", "request POST /api/notifications/read-all",
		"request POST /api/agents/{name}/resume", "request POST /api/agents/{name}/cancel",
		"request POST /api/agents/{name}/ack", "request POST /api/agents/{name}/retry",
		"request POST /api/agents/{name}/terminal-opened", "request POST /api/repos/rescan",
		"request POST /api/catalog/refresh":
		return &struct{}{}, true
	}
	return nil, false
}

func TestMenubarContract(t *testing.T) {
	dir := fixturesDir(t)
	// The loose entries below compare against a live handler, so the test needs
	// a server. It is the same harness every other test in this package uses.
	s, _ := newRuntimeServer(t)
	raw, err := os.ReadFile(filepath.Join(dir, "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Fixtures []contractEntry `json:"fixtures"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Fixtures) == 0 {
		t.Fatal("contract.json lists no fixtures")
	}
	for _, e := range doc.Fixtures {
		e := e
		if e.Direction == "event" {
			continue // SSE payloads and terminal.open are not request/response JSON bodies
		}
		t.Run(e.File+" "+e.Direction+" "+e.Method+" "+e.Path, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, e.File))
			if err != nil {
				t.Fatal(err)
			}
			var target any
			if e.Direction == "response" && e.Status >= 400 {
				target = &errorEnvelope{}
			} else {
				pattern := s.s.routePattern(e.Method, e.Path)
				key := e.Direction + " " + e.Method + " " + pattern
				var ok bool
				target, ok = typeFor(key)
				if !ok {
					t.Fatalf("no Go type is mapped for %s; add it to typeFor", key)
				}
			}
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			if err := dec.Decode(target); err != nil {
				t.Fatalf("the Go type rejects the menubar fixture: %v", err)
			}
			out, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			missing := missingKeyPaths(t, body, out)
			if len(missing) > 0 {
				t.Fatalf("the Go output drops keys the menubar expects: %v", missing)
			}
			// For the loose (map[string]any-shaped) entries, the round trip
			// proves nothing on its own, so also check a live GET handler's
			// output. POST routes mapped loosely (catalog/refresh) skip
			// this: the point is comparing against a real GET, and a live
			// POST would mutate state this test doesn't own.
			_, looseList := target.(*[]map[string]any)
			_, looseObj := target.(*map[string]any)
			if (looseList || looseObj) && e.Method == "GET" {
				live := s.get(t, e.Path).Body.Bytes()
				if m := missingKeyPaths(t, body, live); len(m) > 0 {
					t.Fatalf("%s drops keys the menubar expects: %v", e.Path, m)
				}
			}
		})
	}
}

// missingKeyPaths walks the fixture and reports every key path absent from got.
// It is one-directional on purpose: the menubar must find every key it expects,
// and a Go type is free to send more. Arrays compare element 0 against element 0,
// because a fixture's array is a sample, not a length assertion.
func missingKeyPaths(t *testing.T, want, got []byte) []string {
	t.Helper()
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	var out []string
	var walk func(path string, want, got any)
	walk = func(path string, want, got any) {
		switch w := want.(type) {
		case map[string]any:
			g, ok := got.(map[string]any)
			if !ok {
				out = append(out, path+" (not an object)")
				return
			}
			keys := make([]string, 0, len(w))
			for k := range w {
				keys = append(keys, k)
			}
			sort.Strings(keys) // a stable failure message
			for _, k := range keys {
				sub, ok := g[k]
				if !ok {
					out = append(out, path+"."+k)
					continue
				}
				walk(path+"."+k, w[k], sub)
			}
		case []any:
			g, ok := got.([]any)
			if !ok {
				out = append(out, path+" (not an array)")
				return
			}
			if len(w) > 0 && len(g) > 0 {
				walk(path+"[0]", w[0], g[0])
			}
		}
		// scalars: presence is all this test checks; values are the fixtures' own.
	}
	walk("", w, g)
	return out
}
