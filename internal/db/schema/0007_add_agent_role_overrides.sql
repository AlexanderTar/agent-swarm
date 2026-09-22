-- 0007_add_agent_role_overrides.sql: add role_overrides JSON to agents
ALTER TABLE agents ADD COLUMN role_overrides TEXT;
