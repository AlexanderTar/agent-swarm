package web

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>Agent Swarm</title>")},
		"favicon.svg":        {Data: []byte("<svg/>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
	}
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHandlerServesFilesAndFallsBackToIndex(t *testing.T) {
	h := Handler(testFS())
	cases := []struct {
		path, want, cache string
	}{
		{"/", "Agent Swarm", "no-cache"},
		{"/index.html", "Agent Swarm", "no-cache"},
		{"/kanban", "Agent Swarm", "no-cache"},
		{"/some/deep/path", "Agent Swarm", "no-cache"},
		{"/assets/app-1a2b.js", "console.log(1)", "public, max-age=31536000, immutable"},
		{"/favicon.svg", "<svg/>", ""},
	}
	for _, c := range cases {
		rec := get(t, h, http.MethodGet, c.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", c.path, rec.Code)
		}
		body, _ := io.ReadAll(rec.Body)
		if !strings.Contains(string(body), c.want) {
			t.Fatalf("%s: body %q", c.path, body)
		}
		if got := rec.Header().Get("Cache-Control"); got != c.cache {
			t.Fatalf("%s: cache %q, want %q", c.path, got, c.cache)
		}
	}
	if ct := get(t, h, http.MethodGet, "/kanban").Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
}

func TestHandlerRefusesAPIAndOtherMethods(t *testing.T) {
	h := Handler(testFS())
	rec := get(t, h, http.MethodGet, "/api/nope")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Fatalf("api fallback: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(t, h, http.MethodPost, "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d", rec.Code)
	}
	if rec := get(t, h, http.MethodHead, "/"); rec.Code != http.StatusOK {
		t.Fatalf("head: %d", rec.Code)
	}
}

func TestHandlerWithoutBuild(t *testing.T) {
	rec := get(t, Handler(fstest.MapFS{}), http.MethodGet, "/")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "make web-build") {
		t.Fatalf("unbuilt: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDistIsEmbedded(t *testing.T) {
	if _, err := fs.Stat(Dist, ".gitkeep"); err != nil {
		t.Fatalf("dist not embedded: %v", err)
	}
}
