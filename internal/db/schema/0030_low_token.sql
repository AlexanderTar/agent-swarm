-- 0029_low_token.sql: low-token mode (SPIKE-35). agents.low_token is the per-orchestrator override
-- (NULL follows the global default, 0 off, 1 on); the sessions columns hold the end-of-turn context sample.
ALTER TABLE agents ADD COLUMN low_token INTEGER;
ALTER TABLE sessions ADD COLUMN context_tokens INTEGER;
ALTER TABLE sessions ADD COLUMN context_window INTEGER;
ALTER TABLE sessions ADD COLUMN context_strikes INTEGER NOT NULL DEFAULT 0;
