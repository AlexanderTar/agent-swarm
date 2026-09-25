-- Plan lint warnings are a snapshot of the exact registered revision.
ALTER TABLE artifact_revisions ADD COLUMN warnings_json TEXT NOT NULL DEFAULT '[]';
