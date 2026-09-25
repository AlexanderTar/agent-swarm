-- 0010_lowercase_git_verify_keys.sql: rewrite the uppercase JSON keys that
-- untagged runtime.GitRef / runtime.Verify emitted into stored JSON.
-- Without tags Go marshals {"Repo":..,"Branch":..,"SHA":..} / {"Cmd":..,"OK":..},
-- but the MCP schema and the web client use lowercase (repo/branch/sha/dirty,
-- cmd/phase/ok/note). An accept_* request whose binding embeds the uppercase
-- form crashes the approval page (sha7 on undefined) and blanks verification
-- lines. Struct tags fix new writes; this rewrites existing rows.
-- Key matches include the trailing colon so values containing e.g. Repo are
-- never touched; re-running on lowercase rows is a no-op.
UPDATE checkpoints SET git_json = REPLACE(REPLACE(REPLACE(REPLACE(git_json,
  '"Repo":', '"repo":'),
  '"Branch":', '"branch":'),
  '"SHA":', '"sha":'),
  '"Dirty":', '"dirty":')
WHERE git_json LIKE '%"Repo":%' OR git_json LIKE '%"SHA":%';

UPDATE checkpoints SET verify_json = REPLACE(REPLACE(REPLACE(REPLACE(verify_json,
  '"Cmd":', '"cmd":'),
  '"Phase":', '"phase":'),
  '"OK":', '"ok":'),
  '"Note":', '"note":')
WHERE verify_json LIKE '%"Cmd":%' OR verify_json LIKE '%"OK":%';

UPDATE requests SET binding_json = REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(binding_json,
  '"Repo":', '"repo":'),
  '"Branch":', '"branch":'),
  '"SHA":', '"sha":'),
  '"Dirty":', '"dirty":'),
  '"Cmd":', '"cmd":'),
  '"Phase":', '"phase":'),
  '"OK":', '"ok":'),
  '"Note":', '"note":')
WHERE binding_json LIKE '%"Repo":%' OR binding_json LIKE '%"SHA":%'
  OR binding_json LIKE '%"Cmd":%' OR binding_json LIKE '%"OK":%';
