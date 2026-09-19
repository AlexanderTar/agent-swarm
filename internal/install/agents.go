package install

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// AgentsOpts is everything Agents needs. Nothing has a production default (S-5,
// S-6): cmd/swarm fills every field in.
type AgentsOpts struct {
	Cfg            Config
	Run            execx.Runner
	HTTP           *http.Client
	MarketplaceURL string
	Installed      func(ctx context.Context) []Kind
	Confirm        func(prompt string) bool
	PluginsOnly    bool
	Out            io.Writer
}

// agentBinaries maps a kind to the CLI whose presence means "installed".
var agentBinaries = map[Kind]string{
	KindClaude: "claude", KindCodex: "codex", KindAgy: "agy", KindCursor: "cursor-agent",
}

// InstalledKinds reports which agents are on this Mac, in Kinds order.
func InstalledKinds(lookPath func(string) (string, error)) func(context.Context) []Kind {
	return func(context.Context) []Kind {
		var out []Kind
		for _, k := range Kinds {
			if _, err := lookPath(agentBinaries[k]); err == nil {
				out = append(out, k)
			}
		}
		return out
	}
}

// Agents runs the per-agent half of swarm install: §20's removal pass first, then
// the writers for the agents that are present, then the binary link and the
// superpowers plugins.
func Agents(ctx context.Context, o AgentsOpts) error {
	installed := o.Installed(ctx)
	if o.PluginsOnly {
		return o.syncPlugins(ctx, installed)
	}

	// 1. Shared v1 state: the updater job and the release folders (§21.3).
	// once wraps Confirm so the user is asked at most one time, and Agents can tell
	// "declined" from "there was nothing to remove" without prompting twice.
	asked, granted := false, false
	once := func(prompt string) bool {
		if o.Confirm == nil {
			return false
		}
		if !asked {
			asked, granted = true, o.Confirm(prompt)
		}
		return granted
	}
	changed, err := RemoveLegacyShared(ctx, o.Cfg, o.Run, once)
	if err != nil {
		return err
	}
	if asked && !granted {
		// Leaving the folders is a choice, not a silent skip.
		fmt.Fprintln(o.Out, DeclinedReleaseRemoval)
	}
	o.report("removed", changed)

	// 2. Per-agent v1 state, for EVERY kind: leftover config outlives an uninstalled
	// CLI, and §20 requires it gone before the name `swarm` is registered again.
	for _, step := range []struct {
		name string
		run  func() ([]string, error)
	}{
		{"claude", func() ([]string, error) { return RemoveLegacyClaude(o.Cfg) }},
		{"codex", func() ([]string, error) { return RemoveLegacyCodex(o.Cfg) }},
		{"cursor", func() ([]string, error) { return RemoveLegacyCursor(o.Cfg) }},
		{"agy", func() ([]string, error) { return RemoveLegacyAgy(ctx, o.Cfg, o.Run) }},
	} {
		changed, err := step.run()
		if err != nil {
			return fmt.Errorf("removing the Agent Swarm 1.x %s integration: %w", step.name, err)
		}
		o.report("removed", changed)
	}

	// 3. Writers, for the agents that are present.
	for _, k := range installed {
		var changed []string
		var err error
		switch k {
		case KindClaude:
			changed, err = WriteClaude(o.Cfg)
		case KindCodex:
			changed, err = WriteCodex(o.Cfg)
		case KindCursor:
			changed, err = WriteCursor(o.Cfg)
		case KindAgy:
			changed, err = WriteAgy(ctx, o.Cfg, o.Run)
		}
		if err != nil {
			return fmt.Errorf("configuring %s: %w", k.Display(), err)
		}
		o.report("wrote", changed)
	}

	// 4. ~/.local/bin/swarm (§19).
	if wrote, err := LinkBinary(o.Cfg); err != nil {
		return err
	} else if wrote {
		o.report("wrote", []string{o.Cfg.LocalBin()})
	}

	// 5. Plugins (§12.4). A failure here never fails the install.
	return o.syncPlugins(ctx, installed)
}

func (o AgentsOpts) syncPlugins(ctx context.Context, installed []Kind) error {
	log, err := os.OpenFile(o.Cfg.Logs("install.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		if err := os.MkdirAll(o.Cfg.Logs(), 0o755); err != nil {
			return err
		}
		if log, err = os.OpenFile(o.Cfg.Logs("install.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
			return err
		}
	}
	defer log.Close()
	p := Plugins{Cfg: o.Cfg, Run: o.Run, HTTP: o.HTTP, MarketplaceURL: o.MarketplaceURL, Log: log}
	for _, r := range p.Sync(ctx, installed) {
		switch {
		case r.Err != nil:
			fmt.Fprintf(o.Out, "✗ %s: %s — %v\n", r.Kind, r.Plugin, r.Err)
		case r.Action == "skip":
			fmt.Fprintf(o.Out, "  skipped %s for %s: %s\n", r.Plugin, r.Kind, r.Detail)
		case r.Action == "update":
			fmt.Fprintf(o.Out, "✓ %s: %s updated\n", r.Kind, r.Plugin)
		case r.Plugin != "":
			fmt.Fprintf(o.Out, "✓ %s: %s installed\n", r.Kind, r.Plugin)
		}
	}
	return nil
}

func (o AgentsOpts) report(verb string, paths []string) {
	if len(paths) == 0 || o.Out == nil {
		return
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(o.Out, "%s %s\n", verb, p)
	}
}
