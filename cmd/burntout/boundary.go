package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burntout/internal/domain/annotation"
	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/model"
)

const boundaryUsage = "usage: burntout boundary add KIND [VALUE] | list [--history] | remove ID|KIND\n" +
	"  shutdown 21:30     no work after 21:30 on workdays\n" +
	"  no-weekend         no work on Saturdays and Sundays\n" +
	"  meetings-max 4     at most 4 hours of meetings on workdays\n" +
	"  focus-block 90     a focus block of at least 90 minutes on workdays\n" +
	"A rule applies from tomorrow; adding a kind you have changes it"

// inputError is a mistake in what the user typed, not a failure to read or
// write the store: the review says it and asks again.
type inputError string

func (e inputError) Error() string { return string(e) }

// isInputError reports whether err from boundaryCommand is the user's to
// fix: a usage mistake, an unknown rule, or an invalid boundary.
func isInputError(err error) bool {
	var ie inputError
	return errors.As(err, &ie) || errors.Is(err, boundary.ErrInvalid)
}

// boundaryStore is where boundaries are kept: the store, or the
// dashboard's control, which opens it per change.
type boundaryStore interface {
	SetBoundary(ctx context.Context, b boundary.Boundary, today string, now time.Time) (int64, bool, error)
	RemoveBoundary(ctx context.Context, id int64, today string, now time.Time) (bool, error)
	Boundaries(ctx context.Context) ([]boundary.Boundary, error)
}

// runBoundary adds, lists and removes boundaries.
func runBoundary(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(boundaryUsage)
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenExisting(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		return errors.New("nothing recorded yet: start the daemon first")
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return boundaryCommand(ctx, store, os.Stdout, args, boundary.SourceCLI, time.Now())
}

func boundaryCommand(ctx context.Context, st boundaryStore, out io.Writer, args []string, source string, now time.Time) error {
	today := model.LogicalDay(now, rollover)
	switch cmd, rest := args[0], args[1:]; cmd {
	case "list":
		history := len(rest) == 1 && (rest[0] == "--history" || rest[0] == "-history")
		if len(rest) > 0 && !history {
			return inputError(boundaryUsage)
		}
		bs, err := st.Boundaries(ctx)
		if err != nil {
			return err
		}
		shown := boundary.Current(bs)
		if history {
			shown = bs
		}
		if len(shown) == 0 {
			_, _ = fmt.Fprintln(out, "No boundaries. Set one with, for example, `burntout boundary add shutdown 21:30`.")
		}
		for _, b := range shown {
			_, _ = fmt.Fprintf(out, "  %3d  %s\n", b.ID, describeBoundary(b, history))
		}
		return nil
	case "add", "set":
		if len(rest) < 1 || len(rest) > 2 {
			return inputError(boundaryUsage)
		}
		value := ""
		if len(rest) == 2 {
			value = rest[1]
		}
		b, err := boundary.Parse(rest[0], value)
		if err != nil {
			return fmt.Errorf("%w\n%s", err, boundaryUsage)
		}
		b.Source = source
		return setBoundary(ctx, st, out, b, today, now)
	case "remove":
		if len(rest) != 1 {
			return inputError(boundaryUsage)
		}
		bs, err := st.Boundaries(ctx)
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			kind := boundary.Kind(normalKind(rest[0]))
			for _, b := range boundary.Current(bs) {
				if b.Kind == kind {
					id = b.ID
				}
			}
			if id == 0 {
				return inputError(fmt.Sprintf("no %s boundary (see `burntout boundary list`)", rest[0]))
			}
		}
		ok, err := st.RemoveBoundary(ctx, id, today, now)
		if err != nil {
			return err
		}
		if !ok {
			return inputError(fmt.Sprintf("no active boundary %d (see `burntout boundary list`)", id))
		}
		_, _ = fmt.Fprintln(out, "Removed. It no longer applies from today; its history stays.")
		return nil
	}
	return inputError(boundaryUsage)
}

// setBoundary sets b and says what changed.
func setBoundary(ctx context.Context, st boundaryStore, out io.Writer, b boundary.Boundary, today string, now time.Time) error {
	bs, err := st.Boundaries(ctx)
	if err != nil {
		return err
	}
	var was *boundary.Boundary
	for _, c := range boundary.Current(bs) {
		if c.Kind == b.Kind {
			was = &c
		}
	}
	_, changed, err := st.SetBoundary(ctx, b, today, now)
	if err != nil {
		return err
	}
	switch {
	case !changed:
		_, _ = fmt.Fprintf(out, "Already set: %s.\n", b.Describe())
	case was != nil:
		_, _ = fmt.Fprintf(out, "Changed: %s (was %s), from tomorrow.\n", b.Describe(), was.Describe())
	default:
		_, _ = fmt.Fprintf(out, "Set: %s, from tomorrow. `burntout review` shows how the week went.\n", b.Describe())
	}
	return nil
}

func normalKind(s string) string {
	b, _ := boundary.Parse(s, "")
	return string(b.Kind)
}

func describeBoundary(b boundary.Boundary, history bool) string {
	s := b.Describe()
	if !history {
		return s
	}
	s += ", from " + b.Since
	if !b.Active() {
		s += " to " + b.Until
	}
	if b.Source == boundary.SourceReview {
		s += " (set in a review)"
	}
	return s
}

// rwStore opens a short-lived read-write connection for each change, as
// `burntout annotate` does while the daemon runs, so a review or dashboard
// left open holds no write connection.
type rwStore struct{ dataDir string }

func (r rwStore) with(ctx context.Context, fn func(*sqlite.Store) error) error {
	store, err := sqlite.OpenExisting(ctx, r.dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return fn(store)
}

func (r rwStore) SetBoundary(ctx context.Context, b boundary.Boundary, today string, now time.Time) (id int64, changed bool, err error) {
	err = r.with(ctx, func(s *sqlite.Store) error {
		id, changed, err = s.SetBoundary(ctx, b, today, now)
		return err
	})
	return id, changed, err
}

func (r rwStore) RemoveBoundary(ctx context.Context, id int64, today string, now time.Time) (ok bool, err error) {
	err = r.with(ctx, func(s *sqlite.Store) error {
		ok, err = s.RemoveBoundary(ctx, id, today, now)
		return err
	})
	return ok, err
}

func (r rwStore) Boundaries(ctx context.Context) (bs []boundary.Boundary, err error) {
	err = r.with(ctx, func(s *sqlite.Store) error {
		bs, err = s.Boundaries(ctx)
		return err
	})
	return bs, err
}

func (r rwStore) AddAnnotation(ctx context.Context, a annotation.Annotation) (id int64, err error) {
	err = r.with(ctx, func(s *sqlite.Store) error {
		id, err = s.AddAnnotation(ctx, a)
		return err
	})
	return id, err
}
