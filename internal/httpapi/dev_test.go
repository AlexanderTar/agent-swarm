package httpapi

import (
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func TestDevSeedRouteAbsentWithoutDev(t *testing.T) {
	e := newEnv(t) // Dev defaults false
	status, body := e.api("POST", "/api/dev/seed", nil)
	if status != 404 {
		t.Fatalf("status = %d %s, want 404", status, body)
	}
	wantErr(t, status, body, 404, "not_found", "Unknown API route.")
}

func TestDevSeedWritesTheFixtureKeysAndIsIdempotent(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.Dev = true })

	status, body := e.api("POST", "/api/dev/seed", nil)
	if status != 200 {
		t.Fatalf("status = %d %s", status, body)
	}
	first := decode[map[string]int](t, body)
	if first["items"] != 16 {
		t.Errorf("items = %d, want 16", first["items"])
	}
	if first["requests"] != 9 {
		t.Errorf("requests = %d, want 9", first["requests"])
	}

	// idempotent: a second call writes nothing more
	status, body = e.api("POST", "/api/dev/seed", nil)
	if status != 200 {
		t.Fatalf("status = %d %s", status, body)
	}
	second := decode[map[string]int](t, body)
	if second["items"] != 0 || second["requests"] != 0 {
		t.Errorf("second seed = %+v, want a no-op", second)
	}

	// a real item created afterwards never collides with a seeded key
	it, err := e.items.Create(bg, items.CreateInput{Type: items.Epic, Title: "New epic"}, items.User("test"))
	if err != nil {
		t.Fatalf("Create after seeding: %v", err)
	}
	if it.Key == "EPIC-12" || it.Key == "EPIC-20" || it.Key == "EPIC-30" {
		t.Errorf("new epic key %s collides with a seeded fixture", it.Key)
	}
}
