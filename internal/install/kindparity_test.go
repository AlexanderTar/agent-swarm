package install_test

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/swarmrun/internal/install"
)

// nativeSuperpowers lists kinds that get superpowers without the marketplace
// plugin matrix: grok installs it natively in WriteGrok and has no marketplace.
var nativeSuperpowers = map[install.Kind]bool{install.KindGrok: true}

// TestKindParityInstall is the install half of the kind-parity guard: every
// kind has a skills dir and link mode, a doctor block, a binary in the Agents
// check and a plugin matrix decision.
func TestKindParityInstall(t *testing.T) {
	c := fakeHome(t)
	superpowers := install.MarketplacePlugin{Name: "superpowers"}
	var looked []string
	d := newTestDoctor(t, c, install.Kinds...)
	d.LookPath = func(bin string) (string, error) { looked = append(looked, bin); return bin, nil }
	checks := d.Checks(context.Background())
	for _, k := range install.Kinds {
		t.Run(string(k), func(t *testing.T) {
			if c.SkillsDir(k) == "" {
				t.Error("Config.SkillsDir has no arm")
			}
			if m := install.SkillLinkMode(k); m != install.Symlink && m != install.Copy {
				t.Errorf("SkillLinkMode = %v, want Symlink or Copy", m)
			}
			// a per-kind doctor block: a check named for the kind beyond the shared superpowers one
			var own int
			for _, ch := range checks {
				if strings.HasPrefix(ch.Name, k.Display()+" ") && !strings.HasSuffix(ch.Name, " superpowers") {
					own++
				}
			}
			if own == 0 {
				t.Error("Doctor.Checks has no block for this kind")
			}
			if install.SupportedBy(superpowers, k) == nativeSuperpowers[k] {
				t.Errorf("superpowers plugin matrix: SupportedBy = %v, native install = %v; a kind is exactly one of the two",
					install.SupportedBy(superpowers, k), nativeSuperpowers[k])
			}
		})
	}
	seen := map[string]bool{}
	for _, b := range looked {
		seen[b] = true
	}
	if seen[""] {
		t.Error("the Agents check looked up an empty binary name: agentBinaries lacks a kind")
	}
	// python3, graphify and tmux lookups share LookPath; the Agents check adds one binary per kind.
	if len(seen) < len(install.Kinds) {
		t.Errorf("Agents check looked up %d distinct binaries, want at least %d: %v", len(seen), len(install.Kinds), looked)
	}
}
