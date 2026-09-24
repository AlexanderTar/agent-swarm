package workflow

import "testing"

func TestTemplatesResolve(t *testing.T) {
	want := map[string][]Step{
		"tdd-reviewed": {
			{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
			{ID: "review", Review: []string{"reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3}},
		},
		"ui-tdd-reviewed": {
			{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
			{ID: "review", Review: []string{"reviewer", "ui_reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3}},
		},
		"design-reviewed": {
			{ID: "design", Run: "designer", Gates: []Gate{GateArtifactDesign}},
			{ID: "review", Review: []string{"ui_reviewer"}, Of: "design", Loop: &Loop{Fix: "design", MaxRounds: 2}},
		},
		"debug": {
			{ID: "fix", Run: "debugger", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
			{ID: "review", Review: []string{"reviewer"}, Of: "fix", Loop: &Loop{Fix: "fix", MaxRounds: 3}},
		},
		"mechanical": {
			{ID: "change", Run: "mechanical", Gates: []Gate{GateCommit, GateVerify}},
		},
		"research": {
			{ID: "research", Run: "researcher", Gates: []Gate{GateArtifactNotes}},
		},
	}

	if len(Templates) != len(want) {
		t.Fatalf("Templates has %d entries, want %d", len(Templates), len(want))
	}

	for name, wantSteps := range want {
		gotSteps, ok := Templates[name]
		if !ok {
			t.Errorf("Templates missing %q", name)
			continue
		}
		if len(gotSteps) != len(wantSteps) {
			t.Errorf("%s: got %d steps, want %d", name, len(gotSteps), len(wantSteps))
			continue
		}
		for i, ws := range wantSteps {
			gs := gotSteps[i]
			if gs.ID != ws.ID || gs.Run != ws.Run || gs.Of != ws.Of {
				t.Errorf("%s[%d]: got %+v, want %+v", name, i, gs, ws)
			}
			if len(gs.Review) != len(ws.Review) {
				t.Errorf("%s[%d]: review got %v, want %v", name, i, gs.Review, ws.Review)
			} else {
				for j := range ws.Review {
					if gs.Review[j] != ws.Review[j] {
						t.Errorf("%s[%d]: review got %v, want %v", name, i, gs.Review, ws.Review)
					}
				}
			}
			if len(gs.Gates) != len(ws.Gates) {
				t.Errorf("%s[%d]: gates got %v, want %v", name, i, gs.Gates, ws.Gates)
			} else {
				for j := range ws.Gates {
					if gs.Gates[j] != ws.Gates[j] {
						t.Errorf("%s[%d]: gates got %v, want %v", name, i, gs.Gates, ws.Gates)
					}
				}
			}
			if (gs.Loop == nil) != (ws.Loop == nil) {
				t.Errorf("%s[%d]: loop got %v, want %v", name, i, gs.Loop, ws.Loop)
			} else if gs.Loop != nil {
				if gs.Loop.Fix != ws.Loop.Fix || gs.Loop.MaxRounds != ws.Loop.MaxRounds {
					t.Errorf("%s[%d]: loop got %+v, want %+v", name, i, gs.Loop, ws.Loop)
				}
			}
		}
	}
}
