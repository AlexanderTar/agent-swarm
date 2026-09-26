package runtime

import (
	"strings"
	"testing"
)

// Batch 3 cycle 4 (spec §5, prefix removal): generated notices carry no
// "[swarm]" prefix. Classification moves to transport origin metadata plus
// the daemon Preamble/ShortPreamble, with IsDaemonPrompt extended to the
// prefix-free envelope openings. Legacy prefixed sessions still classify.

func TestPrefixFreeNoticesCarryNoGeneratedPrefix(t *testing.T) {
	notices := map[string]string{
		"inbox":      Inbox([]InboxItem{{ID: "msg_1", Kind: "note", From: "peer", Summary: "hi"}}, 0, "a", "TASK-1"),
		"pending":    PendingNotice(1, "a", "TASK-1"),
		"compaction": CompactionNotice(),
		"quota":      QuotaResetNotice(),
	}
	for name, n := range notices {
		if strings.Contains(n, "[swarm]") {
			t.Errorf("%s still carries the generated prefix: %q", name, n)
		}
	}
}

// The daemon still recognizes its own prefix-free notices as daemon prompts.
func TestIsDaemonPromptRecognizesPrefixFreeNotices(t *testing.T) {
	notices := []string{
		Inbox([]InboxItem{{ID: "msg_1", Kind: "note", From: "peer", Summary: "hi"}}, 0, "a", "TASK-1"),
		PendingNotice(2, "a", "TASK-1"),
		CompactionNotice(),
		QuotaResetNotice(),
	}
	for _, n := range notices {
		if !IsDaemonPrompt(n) {
			t.Errorf("IsDaemonPrompt(%q) = false, want true", n)
		}
	}
}

// Temporary legacy recognition: in-flight sessions still carry the prefix.
func TestIsDaemonPromptKeepsLegacyPrefixRecognition(t *testing.T) {
	for _, p := range []string{
		"[swarm] 1 new message(s) for a (TASK-1). Call swarm_sync.",
		"[swarm] Durable runtime events for a (TASK-1), 1 pending. Call swarm_sync.",
		"[swarm] Your context was compacted. Call swarm_sync.",
		"[swarm] Quota reset window passed. Resuming.",
	} {
		if !IsDaemonPrompt(p) {
			t.Errorf("IsDaemonPrompt(%q) = false, want true (legacy prefix)", p)
		}
	}
}

// Stripping the envelope prefix never rewrites peer body text: a body that
// itself contains "[swarm]" survives delivery verbatim.
func TestPrefixStripNeverRewritesPeerBody(t *testing.T) {
	body := "[swarm] not a notice, just a peer saying hi"
	got := Inbox([]InboxItem{{ID: "msg_9", Kind: "note", From: "peer", Summary: body}}, 0, "a", "TASK-1")
	if !strings.Contains(got, body) {
		t.Errorf("peer body rewritten: got %q, want it to contain %q", got, body)
	}
}
