package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/export"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// Export runs fn with a source over one read transaction, so everything
// it reads is a single consistent snapshot even while the daemon writes.
func (s *Store) Export(ctx context.Context, fn func(export.Source) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("sqlite: export: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(exportSource{tx: tx})
}

type exportSource struct{ tx *sql.Tx }

// each runs query and calls scan for every row, stopping at the first
// error (including one from the export's writer).
func (e exportSource) each(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := e.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// localTime renders a stored instant in the offset it was recorded with.
func localTime(ms int64, offsetMin int) string {
	return time.UnixMilli(ms).In(time.FixedZone("", offsetMin*60)).Format(time.RFC3339)
}

func (e exportSource) Minutes(ctx context.Context, r export.Range, fn func(export.Minute) error) error {
	return e.each(ctx, `
		SELECT minute_ms, utc_offset_min, logical_day, window_key, COALESCE(app_id, ''), category, attended_s, focused_s, visible_s
		FROM attention_minutes WHERE logical_day BETWEEN ? AND ? ORDER BY minute_ms, window_key`,
		[]any{r.From, r.To}, func(rows *sql.Rows) error {
			var ms int64
			var off int
			var key string
			var m export.Minute
			if err := rows.Scan(&ms, &off, &m.Day, &key, &m.App, &m.Category, &m.AttendedS, &m.FocusedS, &m.VisibleS); err != nil {
				return err
			}
			m.Minute = localTime(ms, off)
			m.Unattributed = key == model.UnattributedWindowKey
			return fn(m)
		})
}

func (e exportSource) Intervals(ctx context.Context, r export.Range, fn func(export.Interval) error) error {
	return e.each(ctx, `
		SELECT start_ms, end_ms, utc_offset_min, logical_day, kind, COALESCE(category, ''), COALESCE(session_key, ''), COALESCE(attrs, '{}')
		FROM intervals WHERE logical_day BETWEEN ? AND ? ORDER BY start_ms, id`,
		[]any{r.From, r.To}, func(rows *sql.Rows) error {
			var start, end int64
			var off int
			var attrs string
			var iv export.Interval
			if err := rows.Scan(&start, &end, &off, &iv.Day, &iv.Kind, &iv.Category, &iv.Session, &attrs); err != nil {
				return err
			}
			var a model.IntervalAttrs
			if json.Unmarshal([]byte(attrs), &a) != nil {
				return nil // a damaged row is skipped, as everywhere else
			}
			iv.Start, iv.End = localTime(start, off), localTime(end, off)
			iv.Command, iv.Subcommand, iv.Exit, iv.Group = a.Argv0, a.Subcommand, a.ExitCode, a.Group
			iv.Project, iv.State, iv.Reason, iv.Agent, iv.Tools = a.ProjectKey, a.State, a.Reason, a.Agent, a.Tools
			return fn(iv)
		})
}

func (e exportSource) Days(ctx context.Context, r export.Range, fn func(export.Day) error) error {
	return e.each(ctx, `
		SELECT d.logical_day, f.registry_ver, f.coverage, f.features, s.model_ver, s.status, s.strain, s.components
		FROM (SELECT logical_day FROM daily_features UNION SELECT logical_day FROM scores) d
		LEFT JOIN daily_features f ON f.logical_day = d.logical_day
		LEFT JOIN scores s ON s.logical_day = d.logical_day
		WHERE d.logical_day BETWEEN ? AND ? ORDER BY d.logical_day`,
		[]any{r.From, r.To}, func(rows *sql.Rows) error {
			var day export.Day
			var reg sql.NullInt64
			var coverage, strainV sql.NullFloat64
			var feats, modelVer, status, components sql.NullString
			if err := rows.Scan(&day.Day, &reg, &coverage, &feats, &modelVer, &status, &strainV, &components); err != nil {
				return err
			}
			day.RegistryVersion = int(reg.Int64)
			if coverage.Valid {
				day.Coverage = &coverage.Float64
			}
			if feats.Valid {
				var sf storedFeatures
				if json.Unmarshal([]byte(feats.String), &sf) == nil {
					day.Features = sf.Values
					present := sf.PresentS
					day.PresentS = &present
				}
			}
			day.ModelVersion, day.Status = modelVer.String, status.String
			if strainV.Valid {
				day.Strain = &strainV.Float64
			}
			if components.Valid {
				var sc storedScore
				if json.Unmarshal([]byte(components.String), &sc) == nil {
					for _, d := range sc.Drivers {
						day.Drivers = append(day.Drivers, d.Feature)
					}
					for _, g := range sc.Guardrails {
						day.Guardrails = append(day.Guardrails, g.Name)
					}
				}
			}
			return fn(day)
		})
}

func (e exportSource) Checkins(ctx context.Context, r export.Range, fn func(export.Checkin) error) error {
	return e.each(ctx, `
		SELECT ts_ms, instrument, answers, COALESCE(score, 0) FROM checkins
		WHERE ts_ms >= ? AND ts_ms < ? ORDER BY ts_ms, id`,
		[]any{r.Start.UnixMilli(), r.End.UnixMilli()}, func(rows *sql.Rows) error {
			var ms int64
			var answers string
			var c export.Checkin
			if err := rows.Scan(&ms, &c.Instrument, &answers, &c.Score); err != nil {
				return err
			}
			if json.Unmarshal([]byte(answers), &c.Answers) != nil {
				return nil
			}
			c.At = time.UnixMilli(ms).In(r.Start.Location()).Format(time.RFC3339)
			return fn(c)
		})
}

func (e exportSource) Annotations(ctx context.Context, r export.Range, fn func(export.Annotation) error) error {
	return e.each(ctx, `
		SELECT start_day, end_day, kind, COALESCE(note, '') FROM annotations
		WHERE end_day >= ? AND start_day <= ? ORDER BY start_day, id`,
		[]any{r.From, r.To}, func(rows *sql.Rows) error {
			var a export.Annotation
			if err := rows.Scan(&a.From, &a.To, &a.Kind, &a.Note); err != nil {
				return err
			}
			return fn(a)
		})
}
