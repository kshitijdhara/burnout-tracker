-- Boundaries the user sets for their own work (system design §8.2). A
-- rule is never updated in place: replacing or removing one sets its
-- until_day, so adherence is judged against the rule of the day and the
-- history shows what changed. source 'review' marks a rule set from the
-- weekly review (§8.6).
CREATE TABLE IF NOT EXISTS boundaries (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL,
  value TEXT NOT NULL,
  since_day TEXT NOT NULL,  -- first logical day it applies to
  until_day TEXT,           -- first day it no longer applies to; NULL while active
  source TEXT NOT NULL,     -- cli | dashboard | review
  created_ms INTEGER NOT NULL,
  ended_ms INTEGER);
CREATE INDEX IF NOT EXISTS boundaries_active ON boundaries(kind) WHERE until_day IS NULL;
