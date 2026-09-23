# Agent Swarm

Plan and deliver software with a team of coding agents you supervise from your Mac.

Agent Swarm is a local tool with three parts:

- **A daemon** (`swarm`) that keeps a board of epics, stories, tasks, bugs and spikes, spawns coding agents in detached tmux sessions, and relays messages and checkpoints between them.
- **A web board** at http://127.0.0.1:7777 with hierarchy, kanban and dependency-graph views.
- **A menu bar app** (Swarm.app) showing running agents, requests that need you, usage per agent, and notifications.

Supported agents: Claude Code (default), Codex, Antigravity CLI (`agy`) and Cursor CLI (`cursor-agent`).

## How work flows

1. **Start a spike.** From the menu bar (New orchestrator), the board (New spike) or `swarm new`. Give it a name, pick feature or debug, and optionally suggest repositories.
2. **Confirm repositories.** The spike works out which repositories the work needs, suggests any you missed, and asks you to confirm. Agents only create worktrees in confirmed repositories.
3. **Answer and approve.** The spike agent brainstorms with the superpowers skills, asks you questions, and sends each spec section for approval. Approvals happen only in the menu bar or on the board.
4. **Get an epic (or a bug).** Once the spec and plan are approved, the spike creates an epic with stories and tasks (or a bug with tasks) and dependencies.
5. **Start delivery.** Press Start orchestrator on the epic. The orchestrator creates worktrees, spawns coders and reviewers, merges, and asks you to accept the result.
6. **Supervise.** Open any agent's terminal from the menu bar or board, pause one agent or everything, and read checkpoints instead of transcripts.

Specs and plans live in `~/.swarm/specs` and `~/.swarm/plans`, not in your repositories. Commits and PRs are made under your name and signature, with no agent attribution.

## Requirements

- macOS 14 or later, logged in to the GUI session
- [Homebrew](https://brew.sh)
- Go 1.27+, tmux 3.6+, Node 22 + pnpm 10 (to build the board), Xcode 16+ command line tools (to build the menu bar app)
- [Ghostty](https://ghostty.org) 1.3+
- [Ollama](https://ollama.com) with `qwen3-embedding:0.6b` (knowledge base search)
- At least one agent CLI, signed in: `claude`, `codex`, `agy`, `cursor-agent`
- Nothing to install for [superpowers](https://github.com/obra/superpowers): `swarm install` installs the superpowers plugins for every agent it finds (Cursor must be signed in first)
- Git commit signing enabled (`commit.gpgsign=true`) in repositories agents work in

```bash
brew install go tmux pnpm
brew install --cask ghostty ollama
ollama pull qwen3-embedding:0.6b
```

## Install

```bash
git clone https://github.com/AlexanderTar/agent-swarm.git
cd agent-swarm
make install        # builds swarm into ~/.swarm/bin and Swarm.app into /Applications
swarm install       # launchd service, tmux config, agent MCP/hooks/skill, first-run checks
swarm doctor        # verify everything
open /Applications/Swarm.app
```

`swarm install` does the following:

- writes `~/Library/LaunchAgents/dev.swarm.daemon.plist` and starts the daemon on 127.0.0.1:7777
- writes `~/.swarm/tmux.conf` (agents run on a private tmux socket, `tmux -L swarm`)
- for each installed agent, installs the superpowers plugins it supports, and adds the `swarm` MCP server, the Swarm hooks (inactive outside Swarm sessions) and the `swarm` skill
- scans your home folder for git repositories (hidden folders, `~/Library`, `~/Music`, `~/Pictures`, `~/Movies`, and `~/Downloads` are skipped by default; change exclusions in Settings)
- removes hooks and config left by Agent Swarm 1.x
- links `swarm` into `~/.local/bin`

The first time the menu bar app opens a terminal, macOS asks for permission to control Ghostty. Allow it. The app also asks to send notifications.

### Code signing and macOS permissions

Local builds are signed with `codesign`. If ad-hoc signing (`-`) is used, macOS binds TCC privacy permissions to the binary's cryptographic hash (`cdhash`), causing permission prompts to re-appear whenever `swarm` is recompiled.

To keep permissions trusted permanently across rebuilds and redeploys, create a persistent local self-signed code-signing certificate named `Swarm Dev`:

```bash
# 1. Create a certificate configuration
cat << 'EOF' > /tmp/swarm-cert.conf
[ req ]
default_bits        = 2048
distinguished_name  = req_dn
prompt              = no
x509_extensions     = v3_code_sign

[ req_dn ]
CN = Swarm Dev

[ v3_code_sign ]
keyUsage = critical, digitalSignature
extendedKeyUsage = critical, codeSigning
basicConstraints = critical, CA:FALSE
EOF

# 2. Generate private key and certificate
openssl req -new -x509 -days 3650 -config /tmp/swarm-cert.conf -nodes -keyout /tmp/swarm-key.pem -out /tmp/swarm-cert.pem
openssl pkcs12 -export -legacy -inkey /tmp/swarm-key.pem -in /tmp/swarm-cert.pem -out /tmp/swarm-cert.p12 -passout pass:swarmdev -name "Swarm Dev"

# 3. Import and trust in your login keychain
security import /tmp/swarm-cert.p12 -k ~/Library/Keychains/login.keychain-db -P swarmdev -T /usr/bin/codesign
security add-trusted-cert -p codeSign -k ~/Library/Keychains/login.keychain-db /tmp/swarm-cert.pem
rm -f /tmp/swarm-cert.conf /tmp/swarm-key.pem /tmp/swarm-cert.pem /tmp/swarm-cert.p12
```

The `Makefile` automatically detects `Swarm Dev` when present in your keychain and uses it to sign `bin/swarm`. You can also override the identity via `SWARM_SIGN_IDENTITY="<Your Certificate Name>"`.

### Upgrading from Agent Swarm 1.x

```bash
swarm migrate --dry-run   # shows what will be imported and archived
swarm migrate
```

The old database is kept as `~/.swarm/swarm-v1.db`. The knowledge base is re-embedded in the background (`swarm kb status`).

## Menu bar app

- The menu bar shows active agents and each enabled agent's 5-hour usage (Cursor shows monthly usage, marked M).
- **Needs you**: answer questions and open approvals.
- **Agents**: orchestrators with their sub-agents; open a terminal or pause any row, or Pause all.
- **Usage**: pick an agent to see all its limits (for example Claude 5h, weekly, and per-model weekly).
- **Notifications**: everything that happened; Read all clears them.
- **Settings**: enabled agents, default agent/model/effort per role (model and effort lists come from each agent and refresh daily), the advisor model, notification categories, concurrency limits (orchestrators have their own limit), repository discovery.

## Board

Open http://127.0.0.1:7777 (or Open board in the menu bar).

- **Hierarchy**: epics, bugs and spikes with their children.
- **Kanban**: tasks (or stories, or top-level items) by status, in swimlanes per top-level item. Drag to change status; Swarm refuses moves that break the rules and says why.
- **Dependencies**: the dependency graph for an item's tree or its neighbourhood.
- **Needs you**: the same requests as the menu bar, with full spec sections for review.
- **Start orchestrator**: on any epic, bug or spike, with agent/model defaults you can override.

## CLI

```text
swarm install [--plugins] [--yes]  set up launchd, tmux, superpowers plugins and agent integrations (--plugins: only install/update plugins) (--yes: remove the Agent Swarm 1.x release folders without asking)
swarm doctor [--legacy]            check prerequisites, agents, auth, superpowers, Ollama, signing (--legacy: report Agent Swarm 1.x state still on this Mac)
swarm status                       daemon, active agents, open requests
swarm new --name N --intent feature|debug [--repo PATH...] [--agent A --model M --effort E] [--request TEXT]
swarm start KEY [--agent A --model M --effort E] [--repo PATH...]
swarm agents [--all]               list agents as a tree
swarm attach NAME                  attach to an agent's tmux session here
swarm pause NAME|--all [--group]   pause an agent, its group, or everything
swarm resume NAME
swarm cancel NAME
swarm retry NAME [--note TEXT]     new attempt for a failed, crashed or interrupted agent
swarm ack NAME                     move a failed, crashed or interrupted agent to history
swarm requests                     list open requests
swarm answer REQ TEXT
swarm approve REQ
swarm confirm-repos REQ PATH... [--comment TEXT]
swarm request-changes REQ COMMENT
swarm items [--type T] [--status S] [-q TEXT]
swarm open [KEY]                   open the board
swarm kb search QUERY | kb status
swarm repos [--rescan] | repos add PATH
swarm usage [--refresh]
swarm migrate [--dry-run | --resume | --rollback]
swarm uninstall                    remove launchd job and agent integrations (keeps data)
```

## Advisor

Every agent can consult a stronger model before big decisions, when stuck, and before finishing. Claude agents use Claude Code's built-in advisor (set with the Advisor default, e.g. Fable). Codex, agy and Cursor agents call `swarm_advise`; Swarm runs the advisor model you chose on a condensed copy of the agent's session and returns the answer. Advice shows up in each item's Checkpoints tab.

## How agents talk to Swarm

Each agent runs `swarm mcp` as an MCP server. Tools: `swarm_sync`, `swarm_checkpoint`, `swarm_ask`, `swarm_send`, `swarm_read`, `swarm_kb`, `swarm_advise` (agents without Claude's built-in advisor), and for orchestrators `swarm_items`, `swarm_artifact`, `swarm_worktree`, `swarm_spawn`, `swarm_control`, `swarm_materialize`. Messages are stored in SQLite first; hooks and short notices only tell the agent to fetch them. Nothing an agent sends counts as your approval.

## Files

| Path | What |
|---|---|
| `~/.swarm/swarm.db` | board, agents, sessions, checkpoints, messages |
| `~/.swarm/kb/` | knowledge base (markdown) |
| `~/.swarm/run/daemon.token` | API token for the board and menu bar |
| `~/.swarm/logs/` | daemon logs |
| `~/.swarm/tmux.conf` | tmux config for agent sessions |
| `~/.swarm/specs`, `~/.swarm/plans` | specs and plans written by spikes |

## Troubleshooting

- **Agent shows "Not signed in"**: run the agent once in a terminal and sign in (`claude`, `codex login`, `agy`, `cursor-agent login`), then Check again in Settings.
- **Hooks fail with `node: command not found`**: the daemon's PATH comes from the launchd plist; rerun `swarm install` after installing Node.
- **Terminal button does nothing**: allow Swarm to control Ghostty in System Settings → Privacy & Security → Automation.
- **Usage shows `--` or a dimmed value**: the source was unreachable or the agent's login expired; open the agent once to refresh its login.
- **Search unavailable**: `ollama pull qwen3-embedding:0.6b`.
- **Commit signing fails in an agent session**: make sure gpg-agent has your key unlocked (sign one commit in any terminal), then resume the agent.
- **See agent sessions directly**: `tmux -L swarm ls`.

## Development

```bash
make test      # go test ./... + web tests + swift test
make e2e       # end-to-end run with the fake agent (no model calls)
make dev       # daemon on :17777 with a scratch ~/.swarm-dev
```

## License

MIT
