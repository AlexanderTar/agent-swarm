package runtime

import (
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

// placeholderTitle is spec Locked Decision 2's item title for a spike
// orchestrator started with no Name: the first non-blank line of request,
// whitespace collapsed, cut at a word boundary to at most 60 runes with "…"
// appended when cut. A single word longer than 60 runes hard-cuts at 59
// runes plus "…".
func placeholderTitle(request string) string {
	var line string
	for _, l := range strings.Split(request, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
			break
		}
	}
	collapsed := strings.Join(strings.Fields(line), " ")
	runes := []rune(collapsed)
	if len(runes) <= 60 {
		return collapsed
	}
	cut := runes[:60]
	lastSpace := -1
	for i, r := range cut {
		if r == ' ' {
			lastSpace = i
		}
	}
	if lastSpace > 0 {
		return string(cut[:lastSpace]) + "…"
	}
	return string(runes[:59]) + "…"
}

// placeholderAgentName is spec Locked Decision 2's agent-name candidate for
// a placeholder title: the kebab of its first four words plus "-orchestrator"
// (the same role suffix defaultName gives), falling back to
// "orchestrator" when that kebab is empty. The result is already kebab-form,
// ready to pass as resolveName's "generated" argument.
func placeholderAgentName(title string) string {
	words := strings.Fields(title)
	if len(words) > 4 {
		words = words[:4]
	}
	slug, err := ids.Kebab(strings.Join(words, " "))
	if err != nil || slug == "" {
		return "orchestrator"
	}
	return slug + "-orchestrator"
}
