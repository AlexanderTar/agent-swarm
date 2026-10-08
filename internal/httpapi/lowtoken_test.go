package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

func TestLowTokenAgentRoute(t *testing.T) {
	e, _ := newRuntimeServer(t)
	rec := e.post(t, "/api/agents/root-orchestrator/low-token", `{"on":true}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out lowTokenAgentWire
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Agent != "root-orchestrator" || out.LowToken == nil || !*out.LowToken || out.Notified != 2 {
		t.Fatalf("response = %+v, want root-orchestrator/true/2", out)
	}
	if rec := e.post(t, "/api/agents/task-worker/low-token", `{"on":true}`); rec.Code != 400 {
		t.Fatalf("worker status = %d: %s", rec.Code, rec.Body)
	}
	if rec := e.post(t, "/api/agents/nope/low-token", `{"on":true}`); rec.Code != 404 {
		t.Fatalf("unknown status = %d: %s", rec.Code, rec.Body)
	}
}

func TestLowTokenAllRoute(t *testing.T) {
	e, _ := newRuntimeServer(t)
	rec := e.post(t, "/api/low-token", `{"on":true}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out lowTokenAllWire
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.On || out.Notified != 2 {
		t.Fatalf("response = %+v, want on/2", out)
	}
	_, b := e.api("GET", "/api/settings", nil)
	if !decode[settings.Settings](t, b).LowTokenMode {
		t.Fatal("GET /api/settings: low_token_mode not on")
	}
}

func TestStateAgentNodeCarriesLowToken(t *testing.T) {
	e, _ := newRuntimeServer(t)
	e.post(t, "/api/agents/root-orchestrator/low-token", `{"on":true}`)
	var st struct {
		Agents []struct {
			Name     string `json:"name"`
			LowToken *bool  `json:"low_token"`
			Eff      *bool  `json:"low_token_effective"`
			Children []struct {
				Name string `json:"name"`
				Eff  *bool  `json:"low_token_effective"`
			} `json:"children"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(e.get(t, "/api/state").Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	for _, a := range st.Agents {
		if a.Name != "root-orchestrator" {
			continue
		}
		if a.LowToken == nil || !*a.LowToken || a.Eff == nil || !*a.Eff {
			t.Fatalf("root node = %+v, want low_token and low_token_effective true", a)
		}
		for _, c := range a.Children {
			if c.Eff == nil || !*c.Eff {
				t.Fatalf("child %s low_token_effective = %v, want true", c.Name, c.Eff)
			}
		}
		return
	}
	t.Fatal("root-orchestrator not in /api/state")
}

func TestPutSettingsNeverChangesLowTokenMode(t *testing.T) {
	e := newEnv(t) // the runtime tree's fake agent kind doesn't validate in PUT
	if err := e.s.Settings.SetLowTokenMode(bg, true); err != nil {
		t.Fatal(err)
	}
	_, b := e.api("GET", "/api/settings", nil)
	cur := decode[settings.Settings](t, b)
	cur.LowTokenMode = false // a PUT that flips it
	if status, body := e.api("PUT", "/api/settings", cur); status != 200 {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	_, b = e.api("GET", "/api/settings", nil)
	if !decode[settings.Settings](t, b).LowTokenMode {
		t.Fatal("PUT with low_token_mode:false changed the stored value")
	}
	// A body lacking the key entirely.
	var raw map[string]any
	json.Unmarshal(b, &raw)
	delete(raw, "low_token_mode")
	if status, body := e.api("PUT", "/api/settings", raw); status != 200 {
		t.Fatalf("PUT = %d: %s", status, body)
	}
	_, b = e.api("GET", "/api/settings", nil)
	if !decode[settings.Settings](t, b).LowTokenMode {
		t.Fatal("PUT without low_token_mode changed the stored value")
	}
}
