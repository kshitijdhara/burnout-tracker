-- Retention deletes by end_ms (intervals) and ts_ms (checkins); without
-- these indexes each nightly run scans the whole table.
CREATE INDEX IF NOT EXISTS intervals_end ON intervals(end_ms);
CREATE INDEX IF NOT EXISTS checkins_ts ON checkins(ts_ms);
