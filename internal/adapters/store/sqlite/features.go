package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

// DataDays returns the logical days in [from, to] with anything recorded,
// in order.
func (s *Store) DataDays(ctx context.Context, from, to string) ([]string, error) {
	var days []string
	err := s.scanRows(ctx, `
		SELECT logical_day FROM attention_minutes WHERE logical_day BETWEEN ?1 AND ?2
		UNION SELECT logical_day FROM intervals WHERE logical_day BETWEEN ?1 AND ?2
		UNION SELECT logical_day FROM health_daily WHERE logical_day BETWEEN ?1 AND ?2
		ORDER BY 1`,
		[]any{from, to}, func(r *sql.Rows) error {
			var d string
			if err := r.Scan(&d); err != nil {
				return err
			}
			days = append(days, d)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: data days: %w", err)
	}
	return days, nil
}

// DayInput loads everything the daily features need for one logical day.
func (s *Store) DayInput(ctx context.Context, logicalDay string) (features.Day, error) {
	date, err := time.Parse(time.DateOnly, logicalDay)
	if err != nil {
		return features.Day{}, fmt.Errorf("sqlite: day input: %w", err)
	}
	d := features.Day{Date: logicalDay, Weekend: date.Weekday() == time.Saturday || date.Weekday() == time.Sunday}
	minutes, err := s.DayMinutes(ctx, logicalDay)
	if err != nil {
		return features.Day{}, err
	}
	for _, m := range minutes {
		d.Minutes = append(d.Minutes, features.Minute{
			At: m.Minute, Label: m.Label, Category: m.Category, AttendedS: m.AttendedS, CallS: m.CallS, VideoS: m.VideoS,
		})
	}
	err = s.scanRows(ctx, `
		SELECT start_ms, end_ms, utc_offset_min, kind, COALESCE(session_key, ''), COALESCE(attrs, '{}')
		FROM intervals WHERE logical_day = ? AND kind IN (?, ?, ?, ?) ORDER BY start_ms`,
		[]any{logicalDay, string(model.IntervalPresence), string(model.IntervalShellCmd),
			string(model.IntervalAgentState), string(model.IntervalHumanState)},
		func(r *sql.Rows) error {
			var start, end int64
			var off int
			var kind, session, attrs string
			if err := r.Scan(&start, &end, &off, &kind, &session, &attrs); err != nil {
				return err
			}
			var a model.IntervalAttrs
			if err := json.Unmarshal([]byte(attrs), &a); err != nil {
				return nil // a damaged row is skipped, not fatal
			}
			zone := time.FixedZone("", off*60)
			sp := features.Span{
				Start: time.UnixMilli(start).In(zone), End: time.UnixMilli(end).In(zone),
				State: a.State, Session: session, Argv0: a.Argv0, Group: a.Group, Reason: a.Reason,
			}
			if a.ExitCode != nil {
				sp.Exit = *a.ExitCode
			}
			switch model.IntervalKind(kind) {
			case model.IntervalPresence:
				d.Presence = append(d.Presence, sp)
				d.Present += sp.Duration()
			case model.IntervalShellCmd:
				d.Shell = append(d.Shell, sp)
			case model.IntervalAgentState:
				d.Agent = append(d.Agent, sp)
			case model.IntervalHumanState:
				d.Human = append(d.Human, sp)
			}
			return nil
		})
	if err != nil {
		return features.Day{}, fmt.Errorf("sqlite: day input: %w", err)
	}

	prev := date.AddDate(0, 0, -1).Format(time.DateOnly)
	var lastMs sql.NullInt64
	var lastOff sql.NullInt64
	err = s.db.QueryRowContext(ctx, `
		SELECT end_ms, utc_offset_min FROM intervals WHERE logical_day = ? AND kind = ?
		ORDER BY end_ms DESC LIMIT 1`, prev, string(model.IntervalPresence)).Scan(&lastMs, &lastOff)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return features.Day{}, fmt.Errorf("sqlite: day input: %w", err)
	case lastMs.Valid:
		d.PrevLastPresence = time.UnixMilli(lastMs.Int64).In(time.FixedZone("", int(lastOff.Int64)*60))
	}

	// Coverage comes from the day's health record. Days recorded before
	// it existed count as fully observed.
	d.Coverage = 1
	if slis, err := s.Health(ctx, logicalDay); err != nil {
		return features.Day{}, err
	} else if slis != "" {
		var h struct {
			Coverage *float64 `json:"coverage"`
		}
		if json.Unmarshal([]byte(slis), &h) == nil && h.Coverage != nil {
			d.Coverage = *h.Coverage
		}
	}
	return d, nil
}

// storedFeatures is the JSON in daily_features.features.
type storedFeatures struct {
	PresentS float64            `json:"present_s"`
	Excluded bool               `json:"excluded,omitempty"`
	Values   map[string]float64 `json:"values"`
}

// SaveFeatureDays stores days' features under the current registry
// version, replacing what was there.
func (s *Store) SaveFeatureDays(ctx context.Context, days []strain.Day) error {
	if err := s.writable(); err != nil {
		return err
	}
	if len(days) == 0 {
		return nil
	}
	return s.inTx(ctx, "save features", func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO daily_features (logical_day, registry_ver, coverage, features) VALUES (?, ?, ?, ?)
			ON CONFLICT (logical_day) DO UPDATE SET
				registry_ver = excluded.registry_ver, coverage = excluded.coverage, features = excluded.features`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, d := range days {
			b, err := json.Marshal(storedFeatures{PresentS: d.Present.Seconds(), Excluded: d.Excluded, Values: d.Values})
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx, d.Date, features.RegistryVersion, d.Coverage, string(b)); err != nil {
				return err
			}
		}
		return nil
	})
}

// FeatureDays returns stored features for days in [from, to], in order.
// Rows from another registry version are left out, so callers recompute
// them.
func (s *Store) FeatureDays(ctx context.Context, from, to string) ([]strain.Day, error) {
	var out []strain.Day
	err := s.scanRows(ctx, `
		SELECT logical_day, coverage, features FROM daily_features
		WHERE logical_day BETWEEN ? AND ? AND registry_ver = ? ORDER BY logical_day`,
		[]any{from, to, features.RegistryVersion}, func(r *sql.Rows) error {
			var d strain.Day
			var raw string
			if err := r.Scan(&d.Date, &d.Coverage, &raw); err != nil {
				return err
			}
			var sf storedFeatures
			if err := json.Unmarshal([]byte(raw), &sf); err != nil {
				return nil // recomputed by the caller
			}
			d.Present = time.Duration(sf.PresentS * float64(time.Second))
			d.Excluded, d.Values = sf.Excluded, sf.Values
			out = append(out, d)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: feature days: %w", err)
	}
	return out, nil
}

// storedScore is the JSON in scores.components.
type storedScore struct {
	Composite  float64                    `json:"composite"`
	Groups     map[features.Group]float64 `json:"groups,omitempty"`
	Drivers    []strain.Driver            `json:"drivers,omitempty"`
	Guardrails []strain.Guardrail         `json:"guardrails,omitempty"`
	Reason     string                     `json:"reason,omitempty"`
	UsableDays int                        `json:"usable_days"`
}

// SaveScores stores scores, replacing earlier ones for the same days.
func (s *Store) SaveScores(ctx context.Context, scores []strain.Score) error {
	if err := s.writable(); err != nil {
		return err
	}
	if len(scores) == 0 {
		return nil
	}
	return s.inTx(ctx, "save scores", func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO scores (logical_day, model_ver, strain, status, components) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (logical_day) DO UPDATE SET
				model_ver = excluded.model_ver, strain = excluded.strain,
				status = excluded.status, components = excluded.components`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, sc := range scores {
			b, err := json.Marshal(storedScore{
				Composite: sc.Composite, Groups: sc.Groups, Drivers: sc.Drivers,
				Guardrails: sc.Guardrails, Reason: sc.Reason, UsableDays: sc.UsableDays,
			})
			if err != nil {
				return err
			}
			var value sql.NullFloat64
			if sc.Status == strain.StatusOK {
				value = sql.NullFloat64{Float64: sc.Strain, Valid: true}
			}
			if _, err := stmt.ExecContext(ctx, sc.Date, strain.ModelVersion, value, string(sc.Status), string(b)); err != nil {
				return err
			}
		}
		return nil
	})
}
