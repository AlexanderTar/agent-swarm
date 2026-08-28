# Agent Swarm Board

Local-first agent orchestration: Kanban board, MCP handoffs, semantic knowledge base under `~/.swarm`, and plugins for Claude Code, Cursor, Codex, and Antigravity.

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
- **@swarm/core** — SQLite + sqlite-vec, tasks, KB, Ollama client
- **swarm-mcp** — stdio proxy + session lifecycle for hookless agents
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
swarm open
```

## MCP tools

`swarm_board`, `swarm_task_get`, `swarm_task_create`, `swarm_task_stage`, `swarm_handoff`, `swarm_pickup`, `swarm_kb_search`, `swarm_kb_get`, `swarm_kb_write`

## License

MIT
