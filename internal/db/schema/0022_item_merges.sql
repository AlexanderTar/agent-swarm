-- 0022_item_merges.sql: per-repo PR / local-merge tracking for finishing a top-level item
-- (docs/specs/2026-09-29-finish-with-pr.md); unfreeze open accept questions so they re-freeze
-- with the new finish options.
CREATE TABLE item_merges (
  id                    TEXT PRIMARY KEY,
  item_id               TEXT NOT NULL REFERENCES items(id),
  integrated_checkpoint TEXT NOT NULL REFERENCES checkpoints(id),
  repo                  TEXT NOT NULL,          -- repo name, as in the checkpoint's GitRef.repo
  repo_id               TEXT NOT NULL REFERENCES repos(id),
  kind                  TEXT NOT NULL CHECK (kind IN ('pr','local')),
  url                   TEXT,                   -- pr only
  number                INTEGER,                -- pr only
  base                  TEXT NOT NULL,          -- repo default branch
  head                  TEXT NOT NULL,          -- integrated branch
  auto_merge            INTEGER NOT NULL DEFAULT 0,
  state                 TEXT NOT NULL CHECK (state IN ('open','merged','closed')),
  checks                TEXT NOT NULL DEFAULT '' CHECK (checks IN ('','pending','passing','failing')),
  merged_sha            TEXT,
  checked_at            INTEGER,
  created_at            INTEGER NOT NULL,
  UNIQUE (item_id, integrated_checkpoint, repo)
);
CREATE INDEX item_merges_open ON item_merges(state, kind);

UPDATE requests SET binding_json = json_remove(binding_json, '$.question', '$.header')
 WHERE state = 'open' AND kind IN ('accept_epic', 'accept_fix') AND binding_json IS NOT NULL;
