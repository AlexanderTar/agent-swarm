package install_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoots are the top-level entries a backticked path must start with to count
// as repo-rooted. Home paths (~/...), absolute paths, globs and <placeholders>
// are not checked.
var repoRoots = []string{"internal/", "cmd/", "web/", "apps/", "docs/", "assets/", "scripts/", "skills/", "relay/", "testdata/"}

// TestDocPathsExist keeps the README and the new-agent runbook from naming files
// that moved or never existed.
func TestDocPathsExist(t *testing.T) {
	span := regexp.MustCompile("`([^`\\s]+)`")
	for _, doc := range []string{"README.md", "docs/adding-an-agent-kind.md"} {
		body, err := os.ReadFile(filepath.Join("..", "..", doc))
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		checked := 0
		for _, m := range span.FindAllStringSubmatch(string(body), -1) {
			p := strings.TrimRight(m[1], ".,;:")
			if !isRepoPath(p) {
				continue
			}
			checked++
			if _, err := os.Stat(filepath.Join("..", "..", p)); err != nil {
				t.Errorf("%s names %s, which does not exist", doc, p)
			}
		}
		if checked == 0 {
			t.Errorf("%s: no repo paths found; the path matcher is broken", doc)
		}
	}
}

func isRepoPath(p string) bool {
	if strings.ContainsAny(p, "*<>$(){}|=") {
		return false
	}
	for _, r := range repoRoots {
		if strings.HasPrefix(p, r) {
			return true
		}
	}
	return p == "README.md" || p == "AGENTS.md"
}

// section returns the text under the first heading that starts with prefix, up
// to the next heading of the same or higher level.
func section(body, prefix string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, prefix) {
			continue
		}
		level := len(l) - len(strings.TrimLeft(l, "#"))
		for j := i + 1; j < len(lines); j++ {
			if n := len(lines[j]) - len(strings.TrimLeft(lines[j], "#")); n > 0 && n <= level && strings.HasPrefix(lines[j], strings.Repeat("#", n)+" ") {
				return strings.Join(lines[i:j], "\n")
			}
		}
		return strings.Join(lines[i:], "\n")
	}
	return ""
}

// TestDocsCoverGrok pins the grok text the README and the runbook must carry.
func TestDocsCoverGrok(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	readme, runbook := read("README.md"), read("docs/adding-an-agent-kind.md")
	supported := "Supported agents:" + strings.SplitN(strings.SplitN(readme, "Supported agents:", 2)[1], "\n", 2)[0]
	for _, c := range []struct{ where, body, text string }{
		{"the supported list", supported, "Grok"},
		{"the keychain/usage table", section(readme, "### Keychain access"), "| Grok |"},
		{"## Advisor", section(readme, "## Advisor"), "Grok"},
		{"## Troubleshooting", section(readme, "## Troubleshooting"), "grok login"},
		{"## Troubleshooting", section(readme, "## Troubleshooting"), "--debug-file"},
	} {
		if !strings.Contains(c.body, c.text) {
			t.Errorf("README.md: %s lacks %q", c.where, c.text)
		}
	}
	lessons := section(runbook, "From grok")
	if lessons == "" {
		lessons = runbook[strings.Index(runbook, "From grok"):]
	}
	for _, want := range []string{
		"One leader per agent", "GROK_HOME", "grokhome.Env", "auth.json", "never disabled", "[skills] paths",
		"cli-chat-proxy.grok.com/v1/billing", "inline auth flow", "additionalContext", "installed-plugins",
	} {
		if !strings.Contains(lessons, want) {
			t.Errorf("runbook grok lessons lack %q", want)
		}
	}
}
