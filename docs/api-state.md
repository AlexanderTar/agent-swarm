# GET /api/state: bounded snapshot

The menu bar refetches `GET /api/state` on every daemon event, so the response must stay small and cheap whatever the history. `agentNodeOut` (`internal/httpapi/runtime.go`) costs about ten DB queries per node. On 2026-10-23 the unbounded response was 3.6 MB and 28–49 s: 3966 nodes for 249 top-level agents, 241 of them finished.

## Contract (extends contracts §3.2 AgentNode and §4 state)

- `/api/state` trims finished agents; `/api/agents` and `/api/items/{key}` do not and return the full tree.
- Per parent, `finished` holds at most 10 finished children, newest first by `finished_at` (else `created_at`). The top-level `agents` list holds at most 10 finished top-level agents.
- A finished node keeps only the finished children that lead to a live agent; its other finished descendants are dropped.
- A non-finished agent, and every ancestor of one, is never dropped, so `active_count` is the same as for the untrimmed tree. Finished means `agents.state` is `finished` or `acknowledged`; a child whose session ended while its agent state is still active is never trimmed, so in that rare case `finished` can hold more than 10.
- Each node carries `finished_count`: the untrimmed number of finished children. The state carries `finished_count`: the untrimmed number of finished top-level agents. The node field is absent on `/api/agents` and `/api/items/{key}`; clients fall back to `finished.length` when a daemon doesn't send them.
- `finished` is always `[]`, never null.

The trim is `pruneFinishedForState`, which runs on the flat agent list before `buildAgentTree`, so pruned agents cost no queries. Tests: `internal/httpapi/state_prune_test.go`.

## Rule for agents changing /api/state

- Never add per-node DB work for finished agents, and never build a node and then drop it. Prune the flat list first; keep the response bounded by the caps above, not by history.
- Anything that adds a field per node must be cheap for a node that isn't live.

## Measuring

```sh
curl -s -o /dev/null -w 'bytes=%{size_download} time=%{time_total}s\n' \
  -H "Authorization: Bearer $(cat ~/.swarm/run/daemon.token)" \
  "http://127.0.0.1:$(cat ~/.swarm/run/daemon.port)/api/state"
```

Expect well under 1 MB and a few seconds. Count nodes with `... | jq '[.agents | .. | objects | select(has("finished_count"))] | length'` (start at `.agents`: the state object carries `finished_count` too). Run the tests with `go test ./internal/httpapi -run 'PruneFinished|StateTrims|StateFinishedCount|StateNeverBuilds'`.
