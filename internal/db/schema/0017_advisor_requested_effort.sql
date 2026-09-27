-- Keep the user's advisor effort even while native Claude advice has no
-- separate advisor process and therefore no effective advisor_effort.
ALTER TABLE agents ADD COLUMN advisor_requested_effort TEXT;

-- Existing simulated rows already stored the requested effort directly.
UPDATE agents SET advisor_requested_effort = advisor_effort
WHERE advisor_effort IS NOT NULL AND advisor_effort <> '';

-- Older native rows may still carry an effective effort from before native
-- mode stopped configuring a separate advisor. Preserve it above, then clear.
UPDATE agents SET advisor_effort = NULL WHERE advisor_mode = 'native';
