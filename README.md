# Agent Swarm Board

Local-first coordination for coding agents: a Kanban board the agents write to themselves, MCP
handoffs, a semantic knowledge base under `~/.swarm`, and plugins for Claude Code, Cursor, Codex,
Antigravity and OpenCode.

## How it works

Hooks track your session — they never put anything on the board. When an agent starts work worth
coordinating it calls `swarm_task_create` with a title and summary **it writes itself**, and the
session attaches to that item. Other agents join the same item with `swarm_task_join`, so one tile
can carry several agents, models and conversations. Every new item, the `.md` files it produces and
the agent transcripts behind it are embedded into `~/.swarm/kb` for semantic search.

Ollama is used for embeddings (`nomic-embed-text`) and for the board's chat Console (`qwen3:4b`).
It does not name or summarise your work.

## Quick start

```bash
# Bootstrap (from repo)
pnpm install
pnpm build
node packages/cli/dist/index.js install

# Or one-liner (after publishing)
curl -fsSL https://raw.githubusercontent.com/alexandertar/agent-swarm/main/install.sh | sh
```

Board: http://127.0.0.1:7777

## Architecture

- **swarmd** — Fastify daemon (hooks, REST, WebSocket, MCP, static UI)
- **@swarm/core** — SQLite + sqlite-vec, tasks, sessions, KB indexer, Ollama client
- **swarm-mcp** — stdio proxy; tells the daemon which session is calling
- **plugin/** — multi-agent plugin bundle

## Requirements

- Node 20+
- Ollama with `nomic-embed-text` and `qwen3:4b`
- macOS (launchd jobs)

## CLI

```bash
swarm install      # wizard
swarm doctor       # health checks
swarm doctor --hooks
swarm status
swarm update       # pull latest tag
swarm plugin sync
swarm kb reindex   # re-embed ~/.swarm/kb
swarm open
```

## MCP tools

| tool | what it does |
|---|---|
| `swarm_board` | read the board, filter by status / repo / agent / tag |
| `swarm_task_get` | one item with its tags, sessions, events and subtasks |
| `swarm_task_create` | create an item you name and summarise yourself |
| `swarm_task_update` | keep title, summary, status and tags current |
| `swarm_task_join` | put another session on the same item |
| `swarm_task_stage` | move, claim, release, block, complete, heartbeat, archive |
| `swarm_handoff` | structured handoff note, moves the item to ready |
| `swarm_pickup` | list open handoffs or claim one exclusively |
| `swarm_memory_write` | save a durable fact to the knowledge base |
| `swarm_memory_search` | search durable memories |
| `swarm_kb_search` / `swarm_kb_get` / `swarm_kb_write` | the knowledge base itself |

## License

MIT
