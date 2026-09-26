# Plan: fix agy (and cursor) model passthrough

Spec: `docs/specs/2026-09-26-agy-launch-model.md`. TDD order below; each task
is red -> green -> commit.

## Task 1: `resolveLaunchModel` resolves an agy base+effort to the launch id

**Test** (`internal/runtime/agents_test.go`): add an `agy` catalog row (via a
new `seedAgyCatalog(t, d)` helper mirroring `seedFakeCatalog`) with
`launch_ids: {"medium": "gemini-3.8-flash-medium", "high":
"gemini-3.8-flash-high"}`, `default_effort: "high"`, `effort_encoding:
"slug"`. New test `TestResolveLaunchModelAppliesAgyEffortSuffix`:
- `s.resolveLaunchModel(ctx, Agy, "gemini-3.8-flash", "")` == `"gemini-3.8-flash-high"` (default effort).
- `s.resolveLaunchModel(ctx, Agy, "gemini-3.8-flash", "medium")` == `"gemini-3.8-flash-medium"`.
- `s.resolveLaunchModel(ctx, Agy, "gemini-3.8-flash-high", "")` == `"gemini-3.8-flash-high"` (already suffixed, not in catalog by that exact id in this fixture -> passthrough).
- `s.resolveLaunchModel(ctx, Fake, "fake-1", "")` == `"fake-1"` (non-slug kind unaffected).

Run `go test ./internal/runtime/ -run TestResolveLaunchModelAppliesAgyEffortSuffix -v`, confirm it fails (method doesn't exist yet -> compile error is the expected RED here since this is a new method, acceptable per TDD-for-Go convention: the failure is "undefined: resolveLaunchModel").

**Implementation**: add to `internal/runtime/agents.go`:
```go
func (s *Store) resolveLaunchModel(ctx context.Context, kind AgentKind, model, effort string) string {
	if s.Catalog == nil || model == "" {
		return model
	}
	models, _, err := s.Catalog.ModelsFor(ctx, kind)
	if err != nil {
		return model
	}
	m, ok := catalog.Find(models, model)
	if !ok {
		return model
	}
	return m.LaunchModel(effort)
}
```
Verify green.

## Task 2: `startSession` launches agy with the resolved model

**Test**: `TestStartSpikeResolvesAgyLaunchModel` in `agents_test.go`. Register
a real `*adapter.Agy` in the test Store's `Adapters` map (Deps.Run stubbed to
satisfy `Installed`/`AuthOK`: return success output for `agy --version` and
`agy models`; write a dummy oauth token file at
`<UserHome>/.gemini/antigravity-cli/antigravity-oauth-token`). Add `"agy"` to
`enabled_agents`. Seed the agy catalog row from Task 1.
`s.StartSpike(ctx, SpikeInput{Kind: Agy, Model: "gemini-3.8-flash", Effort: "",
...})`, then assert `tm.started` contains a launch line with
`--model gemini-3.8-flash-high` (default effort) and not bare
`--model gemini-3.8-flash`.

Run, confirm RED (today's code launches with the raw base id).

**Implementation**: in `startSession` (`agents.go` ~1091), change
`Model: a.Model,` to `Model: s.resolveLaunchModel(ctx, a.Kind, a.Model, a.Effort),`
in the `adapter.Spec{...}` literal. Verify green. Also add the `effort:
"medium"` and already-suffixed-id sub-cases as table entries or separate
tests per the spec's "Test" bullets in the task brief.

## Task 3: agy Resume/retry/pause-resume/usage-fallback share the fix for free

No new production code (all four already call `startSession`). Add a
regression test confirming Resume path: `TestResumeAgyKeepsResolvedLaunchModel`
-- spawn, force a retry/resume (reuse whatever existing helper the pause/retry
tests use, e.g. the pattern in `limits_test.go`/`pause_test.go` that calls
`startSession` with `resume=true`), assert the resumed launch argv also
carries the suffixed id. If an existing resume/retry test fixture is cheap to
extend, prefer extending it over adding a whole new spawn+kill+resume test.

## Task 4: agy Wake carries `--model`

**Test** (`internal/adapter/agy_test.go`): `TestAgyWakeIncludesModel` --
call `newAgy(testDeps(t)).Wake(ctx, WakeTarget{SessionID: "ses_1",
ProviderSessionID: "conv-1", Notice: "hi", Model: "gemini-3.8-flash-high"})`
against a stub `StartEnv` that records argv; assert argv contains
`--model gemini-3.8-flash-high`. Run, confirm RED (`WakeTarget` has no
`Model` field yet -> compile error).

**Implementation**:
- `internal/adapter/adapter.go`: add `Model` to `WakeTarget`.
- `internal/adapter/agy.go`'s `Wake`: append `"--model", w.Model` to the
  `StartEnv` argv when `w.Model != ""`.
Verify green.

## Task 5: wake call sites resolve and pass the model

**Test** (`internal/runtime/wake_test.go`): extend (or add) a test that
spawns an agy session (reusing Task 2/3's fixture), enqueues a pending
message, calls `s.WakeDue(ctx)`, and asserts the fake adapter recorded a
`Wake` call whose `WakeTarget.Model` is the resolved suffixed id. Do the
same shape for `WakeOnQuotaReset`. If `adapter.Fake` doesn't record Wake
calls' `WakeTarget`, extend it minimally (it already has `WakeOK`; add a
`WakeCalls []WakeTarget` slice, mirroring `started` in `fakeTmux`). Run,
confirm RED.

**Implementation**:
- `internal/runtime/wake.go`: `wakeRow` gains `Model, Effort string`;
  `wakeCandidates`'s SELECT adds `a.model, a.effort` and the `Scan` call
  reads them. In `WakeDue`'s native-wake block, build
  `adapter.WakeTarget{..., Model: s.resolveLaunchModel(ctx, r.Kind, r.Model, r.Effort)}`.
- `WakeOnQuotaReset`'s SELECT adds `a.model, a.effort`; scan into local
  vars; pass the same resolved `Model` into its `adapter.WakeTarget{...}`.
Verify green.

## Task 6: cursor -- confirmed no-op, documented

No code change (spec's Locked Decisions). Add one regression test in
`internal/catalog/` or `internal/runtime/` asserting a live-shaped cursor
catalog entry (`EffortEncoding` not `"slug"`) round-trips through
`resolveLaunchModel` unchanged, e.g.
`TestResolveLaunchModelIsNoopForCursor`. This guards the finding, not new
behavior.

## Task 7: full verification

- `go test ./... -count=1`
- `go vet ./...`
- `test -z "$(gofmt -l .)"`
- If `TestLeaderCancelDoesNotStopTheSharedScan` (`internal/repos`) fails,
  rerun that package alone and note the result (known load flake).
- If `TestBoardServedAtRoot` 503s, run `make web-build` first, then rerun.

## Commits

One commit per task (or squash 1-2/4-5 if the diff stays small), spec+plan
committed first as their own commit. Every commit message ends with the
required `Co-Authored-By` trailer.
