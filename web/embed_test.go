package web

import (
	"encoding/base64"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>Swarm</title>")},
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
		{"/", "Swarm", "no-cache"},
		{"/index.html", "Swarm", "no-cache"},
		{"/kanban", "Swarm", "no-cache"},
		{"/some/deep/path", "Swarm", "no-cache"},
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

// iOS freezes a title monogram into the webclip when the touch-icon fetch at
// Share time is slow, so the icon is inlined; this guards it against drift.
func TestIndexInlinesAppleTouchIcon(t *testing.T) {
	png, err := os.ReadFile("public/apple-touch-icon.png")
	if err != nil {
		t.Fatal(err)
	}
	want := `rel="apple-touch-icon" href="data:image/png;base64,` + base64.StdEncoding.EncodeToString(png) + `"`
	for _, path := range []string{"index.html", "dist/index.html"} {
		html, err := os.ReadFile(path)
		if os.IsNotExist(err) && path == "dist/index.html" {
			t.Skip("web not built (cd web && pnpm build)")
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(html), want) {
			t.Errorf("%s: apple-touch-icon is not the inlined public/apple-touch-icon.png", path)
		}
	}
}
