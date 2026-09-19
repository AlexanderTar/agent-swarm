package adapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFakeLaunchRunsTheFakeAgentBinary(t *testing.T) {
	a := NewFake(testDeps(t))
	l, err := a.Launch(Spec{AgentName: "login-form-coder", SessionID: "ses_1", Cwd: "/tmp/w",
		Kickoff: "hi", Bin: "/usr/local/bin/swarm"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/swarm-fake-agent", "--session", "ses_1"}
	if strings.Join(l.Argv[:3], " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v", l.Argv)
	}
}

// The fake is installed, signed in and has superpowers unless a flag says otherwise.
func TestFakeChecksAreConfigurable(t *testing.T) {
	a := NewFake(testDeps(t))
	if v, ok := a.Installed(context.Background()); !ok || v == "" {
		t.Fatalf("Installed() = %q, %v", v, ok)
	}
	if err := a.AuthOK(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.SuperpowersInstalled() {
		t.Fatal("superpowers should be present by default")
	}
	a.NoSuperpowers = true
	if a.SuperpowersInstalled() {
		t.Fatal("NoSuperpowers should turn the check off (e2e scenario 12)")
	}
	a.NotInstalled = true
	if _, ok := a.Installed(context.Background()); ok {
		t.Fatal("NotInstalled should turn the check off")
	}
}

// Its hook shapes mirror claude's, so the e2e harness can drive the hook routes.
func TestFakeHookOutputAndParse(t *testing.T) {
	a := NewFake(testDeps(t))
	b, err := a.HookOutput("SessionStart", HookDecision{Context: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		H struct {
			Name string `json:"hookEventName"`
			Ctx  string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.H.Name != "SessionStart" || m.H.Ctx != "hello" {
		t.Fatalf("output = %s", b)
	}
	in, err := a.ParseHook("PreToolUse", []byte(`{"session_id":"p1","tool_name":"mcp__swarm__swarm_sync"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.ProviderSessionID != "p1" || !in.IsSwarmTool {
		t.Fatalf("parsed = %+v", in)
	}
}

func TestFakeWakeIsNeverNative(t *testing.T) {
	a := NewFake(testDeps(t))
	ok, err := a.Wake(context.Background(), WakeTarget{SessionID: "ses_1"})
	if err != nil || ok {
		t.Fatalf("Wake() = %v, %v; the fake has no native wake", ok, err)
	}
}

func TestFakeModelsReturnsOneEntry(t *testing.T) {
	a := NewFake(testDeps(t))
	ms, err := a.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ID != "fake-1" {
		t.Fatalf("Models() = %+v", ms)
	}
}

func TestFakeResumeAppendsTheProviderSessionID(t *testing.T) {
	a := NewFake(testDeps(t))
	l, err := a.Resume(Spec{SessionID: "ses_1", Bin: "/usr/local/bin/swarm", ProviderSessionID: "prov-9"})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Argv[len(l.Argv)-2]; got != "--resume" {
		t.Fatalf("argv = %v, missing --resume", l.Argv)
	}
	if got := l.Argv[len(l.Argv)-1]; got != "prov-9" {
		t.Fatalf("argv = %v, want provider session id last", l.Argv)
	}
}

func TestFakeStaticDescriptors(t *testing.T) {
	a := NewFake(testDeps(t))
	if len(a.ProcessNames()) != 1 || !a.ProcessNames()[0].MatchString("swarm-fake-agent") {
		t.Fatalf("ProcessNames() = %v", a.ProcessNames())
	}
	if a.StartupDialogs() != nil {
		t.Fatalf("StartupDialogs() = %v, want nil", a.StartupDialogs())
	}
	if want := []string{"Escape"}; len(a.InterruptKeys()) != 1 || a.InterruptKeys()[0] != want[0] {
		t.Fatalf("InterruptKeys() = %v", a.InterruptKeys())
	}
}

// HookOutput's Block branch has two shapes: a Stop decision and every other
// event's PreToolUse-style deny.
func TestFakeHookOutputBlocksStopAndOtherEvents(t *testing.T) {
	a := NewFake(testDeps(t))
	b, err := a.HookOutput("Stop", HookDecision{Block: true, Reason: "no"})
	if err != nil {
		t.Fatal(err)
	}
	var stop struct {
		Decision, Reason string
	}
	if err := json.Unmarshal(b, &stop); err != nil {
		t.Fatal(err)
	}
	if stop.Decision != "block" || stop.Reason != "no" {
		t.Fatalf("Stop block output = %s", b)
	}

	b, err = a.HookOutput("PreToolUse", HookDecision{Block: true, Reason: "denied"})
	if err != nil {
		t.Fatal(err)
	}
	var deny struct {
		H struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(b, &deny); err != nil {
		t.Fatal(err)
	}
	if deny.H.Decision != "deny" || deny.H.Reason != "denied" {
		t.Fatalf("PreToolUse block output = %s", b)
	}

	if b, err := a.HookOutput("SessionStart", HookDecision{}); err != nil || b != nil {
		t.Fatalf("HookOutput with no context and no block = %v, %v, want nil, nil", b, err)
	}
}

// ParseHook with empty stdin (some hook events send none) must not fail.
func TestFakeParseHookAcceptsEmptyStdin(t *testing.T) {
	a := NewFake(testDeps(t))
	in, err := a.ParseHook("Stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	if in.Event != "Stop" || in.ProviderSessionID != "" {
		t.Fatalf("parsed = %+v", in)
	}
}
