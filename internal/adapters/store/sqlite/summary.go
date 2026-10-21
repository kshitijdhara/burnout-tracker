package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

const topN = 5

// NamedSeconds is a label with a duration in seconds.
type NamedSeconds struct {
	Name    string
	Seconds float64
}

// NamedCount is a label with a count.
type NamedCount struct {
	Name  string
	Count int
}

// DaySummary is what `burnout status` shows for one logical day.
type DaySummary struct {
	LogicalDay string

	// CategorySeconds is attended time per activity category, excluding
	// unattributed attention.
	CategorySeconds map[model.Category]float64
	// TopApps are the apps with the most attended time. Private apps have no
	// app id and never appear here.
	TopApps []NamedSeconds
	// UnattributedSeconds is presence that no window clearly held.
	UnattributedSeconds float64

	// ShellCommands counts finished interactive commands; ShellFailures
	// counts those with a non-zero exit code.
	ShellCommands int
	ShellFailures int
	// TopCommands are the most frequent command names (argv0).
	TopCommands []NamedCount

	// AgentStateSeconds is time per agent state (working, blocked_on_human,
	// idle), summed across sessions, so it can exceed wall time.
	AgentStateSeconds map[string]float64
	// HumanStateSeconds is time per human state on the single human timeline.
	HumanStateSeconds map[string]float64
	// AgentSessions is the number of distinct agent sessions seen.
	AgentSessions int
}

// DaySummary aggregates one logical day.
func (s *Store) DaySummary(ctx context.Context, logicalDay string) (DaySummary, error) {
	sum := DaySummary{
		LogicalDay:        logicalDay,
		CategorySeconds:   map[model.Category]float64{},
		AgentStateSeconds: map[string]float64{},
		HumanStateSeconds: map[string]float64{},
	}
	steps := []func(context.Context, *DaySummary) error{
		s.attentionSummary, s.shellSummary, s.timelineSummary,
	}
	for _, step := range steps {
		if err := step(ctx, &sum); err != nil {
			return DaySummary{}, fmt.Errorf("sqlite: day summary: %w", err)
		}
	}
	return sum, nil
}

func (s *Store) attentionSummary(ctx context.Context, sum *DaySummary) error {
	err := s.scanRows(ctx, `
		SELECT window_key = ?, category, SUM(attended_s) FROM attention_minutes
		WHERE logical_day = ? GROUP BY 1, 2`,
		[]any{model.UnattributedWindowKey, sum.LogicalDay},
		func(rows *sql.Rows) error {
			var unattributed bool
			var cat string
			var secs float64
			if err := rows.Scan(&unattributed, &cat, &secs); err != nil {
				return err
			}
			if unattributed {
				sum.UnattributedSeconds += secs
			} else {
				sum.CategorySeconds[model.Category(cat)] += secs
			}
			return nil
		})
	if err != nil {
		return err
	}
	return s.scanRows(ctx, `
		SELECT app_id, SUM(attended_s) AS secs FROM attention_minutes
		WHERE logical_day = ? AND app_id IS NOT NULL AND app_id != ''
		GROUP BY app_id ORDER BY secs DESC, app_id LIMIT ?`,
		[]any{sum.LogicalDay, topN},
		func(rows *sql.Rows) error {
			var ns NamedSeconds
			if err := rows.Scan(&ns.Name, &ns.Seconds); err != nil {
				return err
			}
			sum.TopApps = append(sum.TopApps, ns)
			return nil
		})
}

func (s *Store) shellSummary(ctx context.Context, sum *DaySummary) error {
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(COALESCE(json_extract(attrs, '$.exit'), 0) != 0), 0)
		FROM intervals WHERE logical_day = ? AND kind = ?`,
		sum.LogicalDay, string(model.IntervalShellCmd)).Scan(&sum.ShellCommands, &sum.ShellFailures)
	if err != nil {
		return err
	}
	return s.scanRows(ctx, `
		SELECT json_extract(attrs, '$.argv0') AS cmd, COUNT(*) AS n FROM intervals
		WHERE logical_day = ? AND kind = ? AND cmd IS NOT NULL AND cmd != ''
		GROUP BY cmd ORDER BY n DESC, cmd LIMIT ?`,
		[]any{sum.LogicalDay, string(model.IntervalShellCmd), topN},
		func(rows *sql.Rows) error {
			var nc NamedCount
			if err := rows.Scan(&nc.Name, &nc.Count); err != nil {
				return err
			}
			sum.TopCommands = append(sum.TopCommands, nc)
			return nil
		})
}

func (s *Store) timelineSummary(ctx context.Context, sum *DaySummary) error {
	err := s.scanRows(ctx, `
		SELECT kind, COALESCE(json_extract(attrs, '$.state'), ''), SUM(end_ms - start_ms) / 1000.0
		FROM intervals WHERE logical_day = ? AND kind IN (?, ?) GROUP BY 1, 2`,
		[]any{sum.LogicalDay, string(model.IntervalAgentState), string(model.IntervalHumanState)},
		func(rows *sql.Rows) error {
			var kind, state string
			var secs float64
			if err := rows.Scan(&kind, &state, &secs); err != nil {
				return err
			}
			if model.IntervalKind(kind) == model.IntervalAgentState {
				sum.AgentStateSeconds[state] += secs
			} else {
				sum.HumanStateSeconds[state] += secs
			}
			return nil
		})
	if err != nil {
		return err
	}
	return s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT session_key) FROM intervals
		WHERE logical_day = ? AND kind = ? AND session_key IS NOT NULL AND session_key != ''`,
		sum.LogicalDay, string(model.IntervalAgentState)).Scan(&sum.AgentSessions)
}

func (s *Store) scanRows(ctx context.Context, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Health returns the SLI JSON recorded for a logical day, or "" if none.
func (s *Store) Health(ctx context.Context, logicalDay string) (string, error) {
	var slis string
	err := s.db.QueryRowContext(ctx, `SELECT slis FROM health_daily WHERE logical_day = ?`, logicalDay).Scan(&slis)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: read health: %w", err)
	}
	return slis, nil
}
