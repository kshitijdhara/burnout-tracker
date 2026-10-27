-- At most one active boundary per kind (system design §8.2). SetBoundary
-- runs in an immediate transaction; the unique index makes a second active
-- rule of a kind impossible. Concurrent writes before it could leave two,
-- so all but the newest active rule of a kind are ended first, the day
-- before the newer one starts, as SetBoundary would have ended them.
UPDATE boundaries
SET until_day = MAX(since_day, (
      SELECT date(n.since_day, '-1 day') FROM boundaries n
      WHERE n.kind = boundaries.kind AND n.until_day IS NULL AND n.id > boundaries.id
      ORDER BY n.id LIMIT 1)),
    ended_ms = (
      SELECT n.created_ms FROM boundaries n
      WHERE n.kind = boundaries.kind AND n.until_day IS NULL AND n.id > boundaries.id
      ORDER BY n.id LIMIT 1)
WHERE until_day IS NULL AND EXISTS (
  SELECT 1 FROM boundaries n
  WHERE n.kind = boundaries.kind AND n.until_day IS NULL AND n.id > boundaries.id);
DROP INDEX IF EXISTS boundaries_active;
CREATE UNIQUE INDEX IF NOT EXISTS boundaries_one_active ON boundaries(kind) WHERE until_day IS NULL;
