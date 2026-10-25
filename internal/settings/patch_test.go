package settings

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

func raw(fields map[string]string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range fields {
		out[k] = json.RawMessage(v)
	}
	return out
}

func TestPatchChangesOnlyTheGivenSettings(t *testing.T) {
	s := newStore(t, kinds.Claude, kinds.Codex)
	before, _ := s.Get(ctx)
	before.ScanExcludes = []string{"~/Downloads"}
	if _, err := s.Put(ctx, before); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Patch(ctx, raw(map[string]string{"max_concurrent_agents": "7", "usage_poll_sec": "900"}))
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(ctx)
	if saved.MaxConcurrentAgents != 7 || again.MaxConcurrentAgents != 7 || again.UsagePollSec != 900 {
		t.Fatalf("patched = %+v / stored %+v", saved, again)
	}
	if !slices.Equal(again.ScanExcludes, []string{"~/Downloads"}) || again.Roles[kinds.RoleCoder] != before.Roles[kinds.RoleCoder] {
		t.Fatalf("an unpatched setting changed: %+v", again)
	}
}

func TestPatchRefusesUnknownMistypedAndInvalidValues(t *testing.T) {
	s := newStore(t, kinds.Claude)
	for _, tc := range []struct {
		fields map[string]string
		msg    string
	}{
		{map[string]string{"no_such_setting": "1"}, "no_such_setting isn't a setting that can be changed here."},
		{map[string]string{"low_token_mode": "true"}, "low_token_mode isn't a setting that can be changed here."},
		{map[string]string{"max_concurrent_agents": `"lots"`}, "max_concurrent_agents must be a JSON int."},
		{map[string]string{"max_concurrent_agents": "0"}, ""}, // Put's own validation
	} {
		before, _ := s.Get(ctx)
		_, err := s.Patch(ctx, raw(tc.fields))
		var ve *ValidationError
		if !errors.As(err, &ve) || (tc.msg != "" && ve.Message != tc.msg) {
			t.Errorf("%v: err = %v, want %q", tc.fields, err, tc.msg)
		}
		if after, _ := s.Get(ctx); after.MaxConcurrentAgents != before.MaxConcurrentAgents || after.LowTokenMode != before.LowTokenMode {
			t.Errorf("%v: a refused patch changed the settings", tc.fields)
		}
	}
}

// Two clients patching different settings at once both land: neither write
// starts from a copy read before the other's.
func TestConcurrentPatchesOfDifferentSettingsBothLand(t *testing.T) {
	for range 20 {
		s := newStore(t, kinds.Claude)
		var wg sync.WaitGroup
		for _, f := range []map[string]string{{"usage_poll_sec": "900"}, {"pause_deadline_sec": "120"}} {
			wg.Go(func() {
				if _, err := s.Patch(ctx, raw(f)); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if got, _ := s.Get(ctx); got.UsagePollSec != 900 || got.PauseDeadlineSec != 120 {
			t.Fatalf("usage_poll_sec = %d, pause_deadline_sec = %d: one patch overwrote the other", got.UsagePollSec, got.PauseDeadlineSec)
		}
	}
}
