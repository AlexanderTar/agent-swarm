# Fix Chore Orchestrator Creation and Lift Brief Character Limit

## Context

When attempting to create a chore orchestrator from the Menubar App ("New orchestrator" window with default intent `Chore`), the submission fails with the error banner:
> "Couldn't start orchestrator. Your entries are saved. Spikes start with an intent. Use New spike."

### Root Cause Analysis
1. **Chore Intent Validation in `internal/items/store.go`**:
   The Menubar App initiates a chore orchestrator by posting to `/api/spikes` with `intent: "chore"`.
   In `internal/runtime/agents.go`, `StartSpike` calls:
   ```go
   it, err := s.Items.Create(ctx, items.CreateInput{
       Type:           items.Spike,
       Title:          in.Name,
       SpikeIntent:    in.Intent,
       Brief:          in.Request,
       SuggestedRepos: in.Repos,
   }, items.User("board"))
   ```
   In `internal/items/store.go` (line 217):
   ```go
   if in.Type == Spike && in.SpikeIntent != "feature" && in.SpikeIntent != "debug" {
       return Item{}, errf(CodeBadRequest, "Spikes start with an intent. Use New spike.")
   }
   ```
   Although `SpikeIntent: "chore"` is supported by `internal/runtime/materialize.go` and the database schema (`CHECK (spike_intent IN ('feature','debug','chore'))`), `store.go` omitted `"chore"` from the allowable intents on `items.Spike`.

2. **600 Character Limit on Brief**:
   The brief is constrained to 600 characters in multiple places:
   - `internal/items/store.go`: `validateText` checks `utf8.RuneCountInString(brief) > 600`.
   - SQLite schema (`items` table): `brief TEXT NOT NULL DEFAULT '' CHECK (length(brief) <= 600)`.
   - `web/`: `BRIEF_MAX = 600`, `maxLength={600}` in `Details.tsx`.
   The user explicitly requested that the 600 character limit on the brief be lifted entirely.

---

## Locked Decisions

1. **Allow `chore` as a valid SpikeIntent in `internal/items/store.go`**:
   - `store.go` line 217 must check:
     `if in.Type == Spike && in.SpikeIntent != "feature" && in.SpikeIntent != "debug" && in.SpikeIntent != "chore"`
   - This allows `POST /api/spikes` with `intent: "chore"` to create the spike item and launch the chore orchestrator.

2. **Lift the 600 character limit on `brief` across store validation and database**:
   - In `internal/items/store.go`, remove the 600 character upper bound check in `validateText(title, brief string)`.
   - In `internal/db/schema/0005_lift_brief_limit.sql`, recreate the `items` table without `CHECK (length(brief) <= 600)`.
   - Increment `db.SchemaVersion` from 4 to 5 in `internal/db/db.go`.
   - In `web/`: Lift the 600 limit in `web/src/logic/newItem.ts`, `web/src/panels/Details.tsx`, and `web/src/mock/daemon.ts`.

3. **Test Discipline**:
   - Do NOT delete existing tests.
   - Update `internal/items/store_test.go` and `internal/db/db_test.go` so that long briefs (> 600 characters) are verified to succeed without errors or CHECK constraint violations.
   - Add unit test verifying that creating a spike with `SpikeIntent: "chore"` succeeds.

---

## DB Models & Migrations

### Migration `0005_lift_brief_limit.sql`
Recreate `items` table to remove `CHECK (length(brief) <= 600)`:

```sql
-- 0005_lift_brief_limit.sql: remove 600-character CHECK constraint on items.brief
CREATE TABLE items_new (
  id                   TEXT PRIMARY KEY,
  key                  TEXT NOT NULL UNIQUE,
  type                 TEXT NOT NULL CHECK (type IN ('epic','story','task','bug','spike','chore')),
  parent_id            TEXT REFERENCES items(id) ON DELETE RESTRICT,
  root_id              TEXT NOT NULL REFERENCES items(id),
  title                TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  brief                TEXT NOT NULL DEFAULT '',
  acceptance_json      TEXT NOT NULL DEFAULT '[]',
  status               TEXT NOT NULL CHECK (status IN
                         ('draft','ready','in_progress','blocked','in_review',
                          'awaiting_approval','done','cancelled')),
  status_before_block  TEXT,
  priority             INTEGER NOT NULL DEFAULT 2 CHECK (priority BETWEEN 0 AND 3),
  role_hint            TEXT,
  tdd_exempt           TEXT CHECK (tdd_exempt IN ('docs','config','mechanical-rename','spike-research')),
  confirmed_repos_json TEXT NOT NULL DEFAULT '[]',
  repos_version        INTEGER NOT NULL DEFAULT 0,
  repo_hints_json      TEXT NOT NULL DEFAULT '[]',
  spike_intent         TEXT CHECK (spike_intent IN ('feature','debug','chore')),
  suggested_repos_json TEXT NOT NULL DEFAULT '[]',
  origin_spike_id      TEXT REFERENCES items(id),
  legacy_key           TEXT,
  sort_order           INTEGER NOT NULL DEFAULT 0,
  revision             INTEGER NOT NULL DEFAULT 1,
  archived_at          INTEGER,
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);

INSERT INTO items_new SELECT * FROM items;

DROP TABLE items;

ALTER TABLE items_new RENAME TO items;

CREATE INDEX items_parent ON items(parent_id);
CREATE INDEX items_root   ON items(root_id, status);
CREATE INDEX items_type   ON items(type, status);

CREATE TRIGGER items_fts_ai AFTER INSERT ON items BEGIN
  INSERT INTO items_fts(rowid, key, title, brief) VALUES (new.rowid, new.key, new.title, new.brief);
END;
CREATE TRIGGER items_fts_ad AFTER DELETE ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, key, title, brief) VALUES ('delete', old.rowid, old.key, old.title, old.brief);
END;
CREATE TRIGGER items_fts_au AFTER UPDATE OF key, title, brief ON items BEGIN
  INSERT INTO items_fts(items_fts, rowid, key, title, brief) VALUES ('delete', old.rowid, old.key, old.title, old.brief);
  INSERT INTO items_fts(rowid, key, title, brief) VALUES (new.rowid, new.key, new.title, new.brief);
END;
```

---

## File List

### Modified Files
- `internal/items/store.go`: Add `"chore"` to valid `SpikeIntent` check; remove 600 character check from `validateText`.
- `internal/items/store_test.go`: Add test for `SpikeIntent: "chore"`; update brief length tests to verify long briefs are accepted.
- `internal/db/db.go`: Update `SchemaVersion` from 4 to 5.
- `internal/db/db_test.go`: Update `TestCheckConstraints` to verify long briefs succeed without CHECK constraint failure.
- `web/src/logic/newItem.ts`: Remove `BRIEF_MAX = 600`.
- `web/src/panels/Details.tsx`: Remove `maxLength={600}` on `Editable label={C.brief}`.
- `web/src/mock/daemon.ts`: Remove `.slice(0, 600)` on brief.
- `web/src/panels/NewItemSheet.test.tsx`: Update assertions to reflect lifted limit.
- `web/src/logic/newItem.test.ts`: Update tests.

### New Files
- `internal/db/schema/0005_lift_brief_limit.sql`: Schema migration 5 lifting brief length check.

---

## Verification

1. `go test -v ./internal/items/...`:
   - Verify `Create` with `Type: Spike, SpikeIntent: "chore"` succeeds.
   - Verify `Create` with a 2,000-character `Brief` succeeds.
2. `go test -v ./internal/db/...`:
   - Verify migration 5 applies cleanly on fresh and existing databases.
   - Verify `items` table accepts long briefs.
3. `go test -v ./internal/runtime/...`:
   - Verify `StartSpike` with `Intent: "chore"` succeeds.
4. `make build && make install-daemon`:
   - Verify compilation, signing, and daemon installation.
5. Menubar App Launch Verification:
   - Create a chore orchestrator with a long brief (> 600 chars).
   - Verify it creates and launches without error.
