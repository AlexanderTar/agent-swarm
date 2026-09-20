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
