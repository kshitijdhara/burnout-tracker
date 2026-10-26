-- Logical days a calendar sync covered, so a day without meetings is told
-- apart from a day without a calendar (system design §4.7).
CREATE TABLE IF NOT EXISTS calendar_days (logical_day TEXT PRIMARY KEY);

-- Small settings the store keeps for itself, such as the time forget last
-- deleted from, which calendar syncs never import before.
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
