package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSettingsCommandPrintsAndPatchesOneSetting(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"GET /api/settings":   `{"max_concurrent_agents":4,"enabled_agents":["claude"]}`,
		"PATCH /api/settings": `{"max_concurrent_agents":6,"enabled_agents":["claude","codex"],"instructions":"be brief"}`,
	})
	defer srv.Close()
	var out bytes.Buffer
	if code := run([]string{"settings", "--home", home, "--url", srv.URL}, &out, &out); code != 0 || !strings.Contains(out.String(), "\"max_concurrent_agents\": 4") {
		t.Fatalf("print = %d %q", code, out.String())
	}
	for _, tc := range []struct{ key, value, body, printed string }{
		{"max_concurrent_agents", "6", `{"max_concurrent_agents":6}`, "max_concurrent_agents = 6\n"},
		{"enabled_agents", `["claude","codex"]`, `{"enabled_agents":["claude","codex"]}`, `enabled_agents = ["claude","codex"]` + "\n"},
		{"instructions", "be brief", `{"instructions":"be brief"}`, `instructions = "be brief"` + "\n"},
	} {
		out.Reset()
		if code := run([]string{"settings", "--home", home, "--url", srv.URL, "set", tc.key, tc.value}, &out, &out); code != 0 || out.String() != tc.printed {
			t.Errorf("set %s = %d %q, want %q", tc.key, code, out.String(), tc.printed)
		}
		last := (*got)[len(*got)-1]
		if last.method != "PATCH" || last.body != tc.body || last.via != "cli" {
			t.Errorf("set %s sent %+v, want PATCH %s", tc.key, last, tc.body)
		}
	}
	out.Reset()
	if code := run([]string{"settings", "--home", home, "--url", srv.URL, "set", "only-a-key"}, &out, &out); code != 2 || !strings.Contains(out.String(), "Usage: swarm settings") {
		t.Errorf("bad args = %d %q", code, out.String())
	}
}

func TestSettingsCommandReportsTheDaemonsRefusal(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{})
	defer srv.Close()
	var out bytes.Buffer
	if code := run([]string{"settings", "--home", home, "--url", srv.URL, "set", "x", "1"}, &out, &out); code != 1 || !strings.Contains(out.String(), "swarm:") {
		t.Fatalf("refused = %d %q", code, out.String())
	}
}

func TestNotificationsCommandListsAndMarksRead(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"GET /api/notifications": `[{"id":"ntf_2","level":"attention","kind":"agent.failed","title":"Agent failed","body":"coder couldn't finish TASK-2.","read_at":null,"created_at":1789651920000},
			{"id":"ntf_1","level":"info","kind":"item.completed","title":"Task completed","body":"TASK-1 is complete.","read_at":1789651930000,"created_at":1789651900000}]`,
		"POST /api/notifications/read-all": `{"read":1}`,
	})
	defer srv.Close()
	var out bytes.Buffer
	if code := run([]string{"notifications", "--home", home, "--url", srv.URL, "--unread"}, &out, &out); code != 0 {
		t.Fatalf("code = %d: %s", code, out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "•") || !strings.Contains(lines[1], "Agent failed") ||
		strings.HasPrefix(lines[2], "•") || !strings.Contains(lines[2], "TASK-1 is complete.") {
		t.Fatalf("output:\n%s", out.String())
	}
	if p := (*got)[0].path; p != "/api/notifications?limit=50&unread=1" {
		t.Errorf("path = %s", p)
	}
	out.Reset()
	if code := run([]string{"notifications", "--home", home, "--url", srv.URL, "--read-all"}, &out, &out); code != 0 || out.String() != "Marked 1 read.\n" {
		t.Fatalf("read-all = %d %q", code, out.String())
	}
}

func TestNotificationsCommandWithNone(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{"GET /api/notifications": `[]`})
	defer srv.Close()
	var out bytes.Buffer
	if code := run([]string{"notifications", "--home", home, "--url", srv.URL}, &out, &out); code != 0 || out.String() != "No notifications.\n" {
		t.Fatalf("= %d %q", code, out.String())
	}
}
