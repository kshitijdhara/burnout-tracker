-- Initial schema. See docs/system-design.md §6 and docs/privacy-and-security.md §3.
-- At-rest protection is OS full-disk encryption (ADR-0007); nothing here is
-- encrypted by the app.

-- Redacted events only; short retention (default 7 days).
CREATE TABLE raw_events (
  id      INTEGER PRIMARY KEY,
  recv_ms INTEGER NOT NULL,
  v       INTEGER NOT NULL,
  source  TEXT    NOT NULL,
  kind    TEXT    NOT NULL,
  payload BLOB    NOT NULL
);
CREATE INDEX raw_events_recv ON raw_events(recv_ms);

-- Per-minute attention rollups per window (default 180 days).
CREATE TABLE attention_minutes (
  minute_ms      INTEGER NOT NULL,
  utc_offset_min INTEGER NOT NULL,
  logical_day    TEXT    NOT NULL,
  window_key     TEXT    NOT NULL,
  app_id         TEXT,
  category       TEXT    NOT NULL,
  monitor        INTEGER,
  attended_s     REAL    NOT NULL,
  focused_s      REAL    NOT NULL,
  visible_s      REAL    NOT NULL,
  PRIMARY KEY (minute_ms, window_key)
);
CREATE INDEX attention_day ON attention_minutes(logical_day, category);

-- Timeline intervals (default 180 days).
CREATE TABLE intervals (
  id             INTEGER PRIMARY KEY,
  start_ms       INTEGER NOT NULL,
  end_ms         INTEGER NOT NULL,
  utc_offset_min INTEGER NOT NULL,
  logical_day    TEXT    NOT NULL,
  kind           TEXT    NOT NULL,
  category       TEXT,
  session_key    TEXT,
  attrs          TEXT
);
CREATE INDEX intervals_kind ON intervals(kind, start_ms);
CREATE INDEX intervals_day  ON intervals(logical_day, kind);

CREATE TABLE daily_features (
  logical_day  TEXT    PRIMARY KEY,
  registry_ver INTEGER NOT NULL,
  coverage     REAL    NOT NULL,
  features     TEXT    NOT NULL
);

CREATE TABLE scores (
  logical_day TEXT PRIMARY KEY,
  model_ver   TEXT NOT NULL,
  strain      REAL,
  status      TEXT NOT NULL,
  components  TEXT,
  forecast    TEXT
);

CREATE TABLE checkins (
  id         INTEGER PRIMARY KEY,
  ts_ms      INTEGER NOT NULL,
  instrument TEXT    NOT NULL,
  answers    TEXT    NOT NULL,
  score      REAL
);

CREATE TABLE annotations (
  id        INTEGER PRIMARY KEY,
  start_day TEXT NOT NULL,
  end_day   TEXT NOT NULL,
  kind      TEXT NOT NULL,
  note      TEXT
);

CREATE TABLE monitor_snapshots (
  ts_ms    INTEGER PRIMARY KEY,
  topology TEXT    NOT NULL
);

CREATE TABLE health_daily (
  logical_day TEXT PRIMARY KEY,
  slis        TEXT NOT NULL
);
