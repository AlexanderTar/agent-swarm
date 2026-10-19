-- 0034_bug_issue_url.sql: the GitHub issue (or comment target) a bug report was filed as by `swarm bugs --issue`.
-- Empty until filed; a second --issue run refuses while it is set.
ALTER TABLE bug_reports ADD COLUMN issue_url TEXT NOT NULL DEFAULT '';
