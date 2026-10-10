package httpapi

import (
	"encoding/json"
	"testing"
)

func TestItemDetailCarriesTodosForARootOnly(t *testing.T) {
	e, seed := newRuntimeServer(t)
	var root map[string]any
	if err := json.Unmarshal(e.get(t, "/api/items/"+seed.RootKey).Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	todos, ok := root["todos"].([]any)
	if !ok || len(todos) != 4 { // context + task + integrate + accept
		t.Fatalf("root todos = %v", root["todos"])
	}
	first := todos[1].(map[string]any)
	if first["id"] != seed.TaskKey || first["label"] != seed.TaskKey+" · Task" || first["status"] != "pending" || first["item_key"] != seed.TaskKey {
		t.Fatalf("first todo = %v", first)
	}
	var task map[string]any
	if err := json.Unmarshal(e.get(t, "/api/items/"+seed.TaskKey).Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if _, present := task["todos"]; present {
		t.Fatal("a task must not carry todos")
	}
}

func TestOrchestratorAgentNodeCarriesProgress(t *testing.T) {
	e, seed := newRuntimeServer(t)
	var body map[string]any
	if err := json.Unmarshal(e.get(t, "/api/state").Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var orch map[string]any
	for _, a := range body["agents"].([]any) {
		if n := a.(map[string]any); n["name"] == seed.AgentName {
			orch = n
		}
	}
	if orch == nil {
		t.Fatal("orchestrator missing from /api/state")
	}
	p, ok := orch["progress"].(map[string]any)
	if !ok || p["done"] != float64(1) || p["total"] != float64(4) || p["current"] != seed.TaskKey+" · Task" {
		t.Fatalf("progress = %v", orch["progress"])
	}
	for _, c := range orch["children"].([]any) {
		if _, present := c.(map[string]any)["progress"]; present {
			t.Fatalf("worker node has progress: %v", c)
		}
	}
}
