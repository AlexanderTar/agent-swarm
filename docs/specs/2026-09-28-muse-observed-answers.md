# Spec: Muse native answers verified from Muse's own session log

## Context

Muse's `request_user_input` fires no hook (re-probed on 1.4.0-R4302.1, 2026-09-28), so today a
Muse `swarm_ask kind:"native_answer"` must carry `answer_text` and is stored with
`agent_reported` evidence (`internal/runtime/native.go` `nativeAnswer`, `reported` branch).

The tool is synchronous. The model continues in the same turn with the answer as the tool
result, so it can call `native_answer` right after. The user confirmed that flow stays.

Muse itself records every question and answer in
`~/.local/share/muse/sessions/YYYY/MM/DD/<muse-session-id>/session.jsonl`. Swarm launches Muse
with the real `XDG_DATA_HOME`, so this is the real data dir. Records are JSON lines, with
`payload.event.kind`:

```json
{"kind":"user_input_prompt_requested","prompt_id":"P","tool_name":"request_user_input",
 "questions":[{"id":"sec1_approve","header":"Section 1","question":"… ⟦swarm:req_…⟧",
 "options":[{"label":"Approve"},{"label":"Request changes"}]}]}
{"kind":"user_input_prompt_settled","prompt_id":"P","outcome":"answered",
 "answers":[{"id":"sec1_approve","selected_label":"Request changes","note":"use German instead"}]}
```

Fixtures: `internal/adapter/testdata/muse/session-user-input-1.4.0.jsonl` (no note, 3 lines;
the third line is a task-output record and must be ignored) and
`session-user-input-note-1.4.0.jsonl` (with note).

## Locked decisions

- Muse keeps the same agent flow: native tool, then `native_answer`.
- On `native_answer` from a Muse agent, the daemon first looks up the answer in Muse's session
  log. If it finds a settled, answered prompt whose first question carries the request's
  `⟦swarm:<ref>⟧` token, that answer is the evidence. Provenance is observed, not
  `agent_reported`, and `answer_source` is `muse_session_log`. `answer_text` becomes optional
  for Muse. If given and it disagrees with the log, the log wins, and the mismatch refusal
  rules of `matchDecisionEvidence` apply to the log text.
- No log match (the file is missing, the session id is unknown, the prompt is not settled, or
  the outcome is not `answered`) falls back to today's `agent_reported` path unchanged,
  including the requirement for `answer_text`.
- Cursor is unchanged.
- Read-only access to Muse's files; nothing is written there.

## Types

`internal/adapter/muse.go`:

```go
// ObservedAnswer scans Muse's own session log for the newest settled
// request_user_input prompt whose first question contains ⟦swarm:<ref>⟧.
// ok is false when nothing matches.
func (m *Muse) ObservedAnswer(providerSessionID, ref string) (label, note string, ok bool)
```

- Path: glob `<UserHome>/.local/share/muse/sessions/*/*/*/<providerSessionID>/session.jsonl`.
  If `XDG_DATA_HOME` is set in the daemon env, use `$XDG_DATA_HOME/muse/sessions/...` instead.
- Stream the file line by line (bufio.Scanner with a large buffer, 4 MiB max line). Skip
  lines that fail to parse.
- Match on `strings.Contains(question, "⟦swarm:"+ref+"⟧")`. Use `runtime`'s existing token
  helper if it is importable without a cycle; otherwise build the literal.

`internal/runtime`: an optional interface, checked via `s.Adapters[a.Kind]`:

```go
type observedAnswerer interface {
    ObservedAnswer(providerSessionID, ref string) (label, note string, ok bool)
}
```

`nativeAnswer`, Muse branch: take the provider session id from the caller's session row. If
`ObservedAnswer` returns ok, `responseText = label` or `label + ": " + note`, and
`reported = false` for evidence purposes. `bindEvidence` writes
`$.evidence = <matchDecisionEvidence result>`, `$.answer_source = 'muse_session_log'`, plus
`$.answer_text` when the agent sent one. Otherwise the existing reported path runs.

## User-facing copy

`NativePromptNextStep`: replace "Cursor AskQuestion and Muse request_user_input must include
answer_text exactly as returned by the native tool; this has agent_reported provenance." with
"Cursor AskQuestion must include answer_text exactly as returned by the native tool; this has
agent_reported provenance. Muse request_user_input: call native_answer right after the tool
returns, with answer_text exactly as returned; Swarm checks it against Muse's own session log."

Skills (`skills/swarm-orchestrator/SKILL.md` and `internal/install/skills/swarm-orchestrator/SKILL.md`,
kept identical): same change.

Error message `answer_text is only for Cursor and Muse.`: unchanged.

## File list

- `internal/adapter/muse.go`: `ObservedAnswer`; update the doc comment on hooks.
- `internal/adapter/muse_test.go`: fixture tests (no note → "French"; note → "Request changes"
  + "use German instead"; unknown ref → !ok; unsettled prompt → !ok; missing file → !ok).
- `internal/runtime/native.go`: the interface and the Muse branch; copy.
- `internal/runtime/native_report_test.go`: Muse observed-path tests via a fake adapter
  (observed approve with no answer_text → evidence observed and source `muse_session_log`;
  log says Request changes but decision approve → refused as mismatch; no log → existing
  reported behaviour, still requiring answer_text).
- Skills as above; any tests pinning the copy.

## Verification

1. `go test ./internal/adapter/ ./internal/runtime/ ./internal/install/ ./internal/hook/`
2. `go test ./...`, `go vet ./...`, `gofmt -l .`

## Out of scope

- Opening a Needs-you row when a Muse question is asked (no hook; possible later via tailing).
- Muse native wake.
- Cursor.
