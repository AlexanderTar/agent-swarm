package migrate

import (
	"fmt"
	"sort"
)

// Root is one top-level item §20's table creates. From holds the v1 keys the row
// came from; it is empty for a pure spec-authored container, whose sources became
// stories instead. legacy_key is From[0] when there is one.
type Root struct {
	Title   string
	Type    string // epic | bug | task
	Status  string // §20's table, verbatim
	From    []string
	Stories []Story
	Tasks   []Child // bug roots (and childless task roots) put tasks directly under the root
}

// Story is a story §20 authors. From is its v1 key when the story IS a v1 row;
// otherwise it is empty and the status is derived from the children. BlockedBy
// names another story of the same root by title.
type Story struct {
	Title     string
	From      string
	BlockedBy string
	Tasks     []Child
}

// Child is one imported task. BlockedBy is the v1 key of a sibling that must finish
// first.
type Child struct {
	From      string
	BlockedBy string
}

// keys builds a []Child for the named v1 keys, with no dependencies.
func keys(ks ...string) []Child {
	out := make([]Child, 0, len(ks))
	for _, k := range ks {
		out = append(out, Child{From: k})
	}
	return out
}

// span is SW-<from>…SW-<to>, with no dependencies.
func span(from, to int) []Child {
	var out []Child
	for n := from; n <= to; n++ {
		out = append(out, Child{From: fmt.Sprintf("SW-%d", n)})
	}
	return out
}

// chain is SW-<from>…SW-<to> where each task is blocked by the one before (§20's
// "in order" for the telemetry pipeline).
func chain(from, to int) []Child {
	out := span(from, to)
	for i := 1; i < len(out); i++ {
		out[i].BlockedBy = out[i-1].From
	}
	return out
}

// Table is §20's import table, verified against the live spec
// (~/.superpowers/specs/2026-09-17-agent-swarm-go-orchestrator.md, §20) on
// 2026-09-18: 10 rows, not the 9 in the batch brief's own pasted snippet. Row 2
// ("Full Go API migration ... alternative to SW-673 (endurio-chat)", from SW-710)
// was added to the live table the same day the brief was written; it is
// decision-gated (37 child tasks only get created if this option is chosen over
// SW-673) and carries no children yet, so it is authored here as a plain, childless
// TASK root. Every other source key was confirmed present in the live v4 database
// that day.
var Table = []Root{
	{
		Title: "Migrate the coach agent from Eve to ADK Go (endurio-chat)",
		Type:  "epic", Status: "ready", From: []string{"SW-673"},
		Stories: []Story{
			{Title: "PR A: Qdrant vector store", Tasks: span(674, 679)},
			{Title: "PR B: ai-SDK completion and the unified tool registry",
				BlockedBy: "PR A: Qdrant vector store", Tasks: span(680, 692)},
			{Title: "PR C: Go ADK service, MCP bridge and Eve removal",
				BlockedBy: "PR B: ai-SDK completion and the unified tool registry", Tasks: span(693, 709)},
		},
	},
	{
		Title: "Full Go API migration with in-process ADK agent, alternative to SW-673 (endurio-chat)",
		Type:  "task", Status: "ready", From: []string{"SW-710"},
	},
	{
		Title: "Support, chat attachments and privacy across Endurio",
		Type:  "epic", Status: "ready", From: []string{"SW-661"},
		Stories: []Story{
			// §20 annotates SW-662…SW-665 "as ready" and SW-666 "ready"; all five are
			// in_progress in v1, which A7 maps to ready anyway, so no special case.
			{Title: "endurio-chat backend", Tasks: keys("SW-662", "SW-663", "SW-664", "SW-665", "SW-666")},
			{Title: "endurio-app client", Tasks: keys("SW-667", "SW-668", "SW-669", "SW-670")},
			{Title: "Cross-repo verification", Tasks: keys("SW-671")},
		},
	},
	{
		Title: "Restore end-to-end OpenObserve telemetry in endurio-chat",
		Type:  "epic", Status: "ready", From: []string{"SW-652"},
		Stories: []Story{
			{Title: "Telemetry pipeline", Tasks: chain(653, 660)},
		},
	},
	{
		Title: "Eve launcher memory on Railway (endurio-chat)",
		Type:  "bug", Status: "ready", From: []string{"SW-648"},
		Tasks: []Child{{From: "SW-649"}, {From: "SW-650"}, {From: "SW-651", BlockedBy: "SW-650"}},
	},
	{
		Title: "Indoor trainer workout export", Type: "epic", Status: "ready",
		Stories: []Story{
			{Title: "Chat backend endpoint (.zwo/.erg)", From: "SW-330"},
			{Title: "App side (endurio-app)", From: "SW-362", BlockedBy: "Chat backend endpoint (.zwo/.erg)"},
		},
	},
	{
		Title: "Workout provider adapters", Type: "epic", Status: "ready",
		Stories: []Story{
			{Title: "Wahoo Cloud API adapter", From: "SW-352"},
			{Title: "Polar AccessLink v3 adapter", From: "SW-353"},
			{Title: "Zwift ride attribution via FIT manufacturer + dedup", From: "SW-354"},
			{Title: "COROS provider adapter", From: "SW-355"},
			{Title: "Swim lap-schema widening", From: "SW-358"},
			{Title: "Vendor API procurement: Wahoo, Polar, COROS", From: "SW-360"},
		},
	},
	{
		Title: "Health platform workout ingestion", Type: "epic", Status: "ready",
		Stories: []Story{
			{Title: "Compile the health-sync native code", From: "SW-365"},
			{Title: "Apple Health workout ingestion", From: "SW-356"},
			{Title: "Health Connect workout ingestion (Android)", From: "SW-357"},
			{Title: "Apple Developer Program gated work", From: "SW-361"},
		},
	},
	{Title: "RAG corpus expansion", Type: "epic", Status: "blocked", From: []string{"SW-359"}},
	{Title: "Customer Center appearance colors on RC dashboard", Type: "epic", Status: "blocked", From: []string{"SW-256"}},
}

// NotImported is §20's "Everything else stays only in swarm-v1.db" list of
// top-level tasks. Their children and every archived row stay behind with them.
var NotImported = []string{
	"SW-638", "SW-600", "SW-562", "SW-552", "SW-526", "SW-629", "SW-612",
	"SW-559", "SW-616", "SW-607", "SW-498", "SW-494", "SW-374",
}

// SourceKeys is every v1 key Table reads, sorted so a "missing keys" error is
// stable.
func SourceKeys() []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, k)
	}
	for _, r := range Table {
		for _, k := range r.From {
			add(k)
		}
		for _, c := range r.Tasks {
			add(c.From)
		}
		for _, s := range r.Stories {
			add(s.From)
			for _, c := range s.Tasks {
				add(c.From)
			}
		}
	}
	sort.Strings(out)
	return out
}

// DeriveStoryStatus applies §10.1's derived-story rule to the children's statuses.
// A story with no children is ready.
func DeriveStoryStatus(children []string) string {
	if len(children) == 0 {
		return "ready"
	}
	allFinished, allReviewedOrFinished, anyLive, anyNotCancelled := true, true, false, false
	for _, s := range children {
		switch s {
		case "done", "cancelled":
		case "in_review":
			allFinished = false
		default:
			allFinished, allReviewedOrFinished = false, false
		}
		if s != "cancelled" {
			anyNotCancelled = true
		}
		if s != "ready" && s != "draft" && s != "cancelled" && s != "done" && s != "in_review" {
			anyLive = true
		}
	}
	switch {
	case allFinished && !anyNotCancelled:
		return "cancelled"
	case allFinished:
		return "done"
	case allReviewedOrFinished && anyNotCancelled:
		return "in_review"
	case anyLive:
		return "in_progress"
	default:
		return "ready"
	}
}
