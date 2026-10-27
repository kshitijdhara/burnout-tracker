package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burntout/internal/app"
	"github.com/kshitijdhara/burntout/internal/config"
	"github.com/kshitijdhara/burntout/internal/domain/annotation"
	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/checkin"
	"github.com/kshitijdhara/burntout/internal/domain/features"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/review"
)

// maxLabels is how many uncategorized apps one review asks about.
const maxLabels = 3

// runReview shows the weekly review: the last 7 completed days against the
// 7 before, recurring drivers, limits crossed, check-ins, boundaries and
// suggestions. In a terminal it then offers to set boundaries, mark the
// week and label apps with no category.
func runReview(ctx context.Context, _ []string) error {
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		fmt.Println("Nothing recorded yet. Run `burntout setup`, then `burntout service install`.")
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	now := time.Now()
	cfg := profileConfig()
	w, due, err := weekReview(ctx, store, cfg, now)
	if err != nil {
		return err
	}
	fmt.Print(formatReview(w, due))
	if !interactive() {
		return nil
	}
	apps, err := store.UncategorizedApps(ctx, w.From, w.To)
	if err != nil {
		return err
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	acts := reviewActions{
		store: rwStore{dataDir: dataDir},
		label: func(app string, c model.Category) error { return labelApp(path, app, c) },
		apps:  unlabelled(apps, cfg),
		today: model.LogicalDay(now, rollover),
		now:   now,
	}
	return askReview(ctx, bufio.NewReader(os.Stdin), os.Stdout, w, acts)
}

// interactive reports whether both ends are a terminal, so `burntout review
// | less` prints the review and asks nothing.
func interactive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		if fi, err := f.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

// weekReview builds the review of the week before now, with boundaries,
// and whether the weekly check-in is due. The CLI and the dashboard share
// it.
func weekReview(ctx context.Context, store *sqlite.Store, cfg config.Config, now time.Time) (review.Week, bool, error) {
	opts := scoreOptions(cfg)
	sc, err := app.ScoreDays(ctx, store, now, opts)
	if err != nil {
		return review.Week{}, false, err
	}
	records, err := store.Checkins(ctx, now.AddDate(0, -3, 0))
	if err != nil {
		return review.Week{}, false, err
	}
	today := model.LogicalDay(now, opts.Rollover)
	t, _ := time.Parse(time.DateOnly, today)
	notes, err := store.Annotations(ctx, t.AddDate(0, 0, -7).Format(time.DateOnly), today)
	if err != nil {
		return review.Week{}, false, err
	}
	w := review.Build(sc.Scores, records, notes, today)
	bs, err := store.Boundaries(ctx)
	if err != nil {
		return review.Week{}, false, err
	}
	var days []features.Day
	if len(boundary.Current(bs)) > 0 {
		for back := 7; back >= 1; back-- {
			d, err := store.DayInput(ctx, t.AddDate(0, 0, -back).Format(time.DateOnly))
			if err != nil {
				return review.Week{}, false, err
			}
			days = append(days, d)
		}
	}
	w.AddBoundaries(bs, days, opts.Features)
	due, _ := review.WeeklyDue(records, now)
	return w, due, nil
}

func formatReview(w review.Week, checkinDue bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Week of %s to %s\n\n", w.From, w.To)
	if w.Calibrating {
		fmt.Fprintf(&b, "Strain: still calibrating (%d of 14 days with enough data), so there's no week to compare yet.\n",
			w.CalibratedOf)
	} else {
		fmt.Fprintf(&b, "Strain: %.0f on average over %d scored days", w.Average, w.Scored)
		if w.PrevScored > 0 {
			fmt.Fprintf(&b, " (the week before: %.0f, %+.0f)", w.PrevAverage, w.Average-w.PrevAverage)
		}
		fmt.Fprintf(&b, "\n  Highest: %s, %.0f (%s)\n", w.Peak.Date, w.Peak.Strain, strainBand(w.Peak.Strain))
	}
	for _, a := range w.Annotations {
		fmt.Fprintf(&b, "  Marked: %s\n", describeAnnotation(a))
	}
	if len(w.Drivers) > 0 {
		b.WriteString("\nWhat kept coming up:\n")
		for _, d := range w.Drivers[:min(3, len(w.Drivers))] {
			fmt.Fprintf(&b, "  ↑ %s (%s)\n", d.Label, days(d.Days))
		}
	}
	if len(w.Guardrails) > 0 {
		b.WriteString("\nLimits crossed:\n")
		for _, g := range w.Guardrails {
			fmt.Fprintf(&b, "  ! %s (%s)\n", g.Label, days(g.Days))
		}
	}
	if len(w.Adherence) > 0 {
		b.WriteString("\nYour boundaries:\n")
		for _, a := range w.Adherence {
			fmt.Fprintf(&b, "  • %s: %s\n", upperFirst(a.Boundary.Describe()), describeAdherence(a, w.To))
		}
	}
	b.WriteString("\nCheck-in: ")
	switch {
	case len(w.Checkin) == 0:
		b.WriteString("none yet.")
	default:
		var parts []string
		for _, id := range []string{checkin.CBIWorkID, checkin.CBIPersonalID} {
			r, ok := w.Checkin[id]
			if !ok {
				continue
			}
			inst, _ := checkin.ByID(id)
			part := fmt.Sprintf("%s %.0f (%s)", strings.ToLower(inst.Name), r.Score, checkin.BandOf(r.Score))
			if p, ok := w.PrevCheckin[id]; ok {
				part += fmt.Sprintf(", before %.0f", p.Score)
			}
			parts = append(parts, part)
		}
		b.WriteString(strings.Join(parts, "; ") + ".")
	}
	if checkinDue {
		b.WriteString(" This week's is due: `burntout checkin` (about a minute).")
	}
	b.WriteString("\n")
	if len(w.Suggestions) > 0 {
		b.WriteString("\nOne or two things to try:\n")
		for _, s := range w.Suggestions {
			fmt.Fprintf(&b, "  • %s\n", s)
			for _, o := range w.Offers {
				if o.For == s {
					fmt.Fprintf(&b, "    A boundary for it: `burntout boundary add %s`\n", boundaryArgs(o.Boundary))
				}
			}
		}
	}
	return b.String()
}

// describeAdherence says how a boundary went, gently: kept days first,
// and days that couldn't be judged said plainly rather than counted as
// kept or missed. weekEnd is the week's last day.
func describeAdherence(a review.Adherence, weekEnd string) string {
	var s string
	switch {
	case a.Judged == 0 && a.Boundary.Since > weekEnd:
		return "new, it applies from " + a.Boundary.Since
	case a.Judged == 0:
		s = "no days to go by yet"
	default:
		s = fmt.Sprintf("kept on %d of %s", a.Kept, days(a.Judged))
		if n := len(a.Missed); n > 0 && n <= 3 {
			s += " (not on " + strings.Join(weekdays(a.Missed), ", ") + ")"
		}
	}
	var extra []string
	if a.DaysOff > 0 {
		extra = append(extra, days(a.DaysOff)+" off")
	}
	if a.NoData > 0 {
		extra = append(extra, days(a.NoData)+" without enough data")
	}
	switch {
	case len(extra) == 0:
	case a.Judged == 0:
		s += " (" + strings.Join(extra, ", ") + ")"
	default:
		s += "; " + strings.Join(extra, ", ") + " left out"
	}
	return s
}

func weekdays(dates []string) []string {
	out := make([]string, len(dates))
	for i, d := range dates {
		t, _ := time.Parse(time.DateOnly, d)
		out[i] = t.Format("Mon 2 Jan")
	}
	return out
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// boundaryArgs is a boundary as `burntout boundary add` takes it.
func boundaryArgs(b boundary.Boundary) string {
	if b.Value == "" {
		return b.Kind.Name()
	}
	return b.Kind.Name() + " " + b.Value
}

func days(n int) string {
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}

// reviewActions are the changes the review's questions can make.
type reviewActions struct {
	store interface {
		boundaryStore
		AddAnnotation(ctx context.Context, a annotation.Annotation) (int64, error)
	}
	label func(app string, c model.Category) error // adds a classify.overrides entry
	apps  []sqlite.UncategorizedApp                // most attended first
	today string
	now   time.Time
}

// errStop means the user typed q: the rest of the questions are skipped.
var errStop = errors.New("review stopped")

// askReview asks the review's questions: the offered boundaries, any other
// boundary change, marks for the week, and categories for up to maxLabels
// apps. Enter skips a question and q stops; each answer is saved as it is
// given. Only a broken store or config fails it.
func askReview(ctx context.Context, in *bufio.Reader, out io.Writer, w review.Week, acts reviewActions) error {
	err := askQuestions(ctx, in, out, w, acts)
	if errors.Is(err, errStop) || errors.Is(err, io.EOF) {
		_, _ = fmt.Fprintln(out, "\nStopped. Anything you answered is saved.")
		return nil
	}
	return err
}

func askQuestions(ctx context.Context, in *bufio.Reader, out io.Writer, w review.Week, acts reviewActions) error {
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	say("\nA few questions to finish (Enter skips, q stops).\n")
	setBoundaryFromReview := func(b boundary.Boundary) error {
		b.Source = boundary.SourceReview
		return setBoundary(ctx, acts.store, out, b, acts.today, acts.now)
	}
	for _, o := range w.Offers {
		for {
			say("\nSet a boundary: %s? [y, another value, or Enter] ", o.Boundary.Describe())
			ans, err := readAnswer(in)
			if err != nil {
				return err
			}
			if ans == "" || ans == "n" || ans == "no" {
				break
			}
			b := o.Boundary
			if ans != "y" && ans != "yes" {
				if b, err = boundary.Parse(string(o.Boundary.Kind), ans); err != nil {
					say("%s\n", strings.TrimPrefix(err.Error(), "boundary: invalid: "))
					continue
				}
			}
			if err := setBoundaryFromReview(b); err != nil {
				return err
			}
			break
		}
	}
	for {
		say("\nSet or change a boundary? Like `shutdown 21:30`, `no-weekend`, `meetings-max 4`, `focus-block 90` or `remove shutdown`: ")
		ans, err := readAnswer(in)
		if err != nil {
			return err
		}
		if ans == "" {
			break
		}
		args := strings.Fields(ans)
		if args[0] != "remove" {
			args = append([]string{"add"}, args...)
		}
		if err := boundaryCommand(ctx, acts.store, out, args, boundary.SourceReview, acts.now); err != nil {
			if !isInputError(err) {
				return err // the store failed: don't carry on as if it was saved
			}
			say("%s\n", strings.TrimPrefix(firstLine(err), "boundary: invalid: "))
		}
	}
	for {
		say("\nMark this week? Like `vacation %s %s --note trip` or `on-call yesterday`: ", w.From, w.To)
		ans, err := readAnswer(in)
		if err != nil {
			return err
		}
		if ans == "" {
			break
		}
		a, err := parseAnnotation(splitNote(ans), acts.today)
		if err != nil {
			say("%s\n", strings.TrimPrefix(err.Error(), "annotation: invalid: "))
			continue
		}
		if _, err := acts.store.AddAnnotation(ctx, a); err != nil {
			return err
		}
		say("Marked: %s\n", describeAnnotation(a))
	}
	if len(acts.apps) == 0 {
		return nil
	}
	choices := labelChoices()
	say("\nThese apps had no category this week. Pick one for each, or Enter to skip:\n  %s\n", strings.Join(choices, ", "))
	for _, a := range acts.apps[:min(maxLabels, len(acts.apps))] {
		for {
			say("  %s (%s): ", printable(a.App), hm(a.Seconds))
			ans, err := readAnswer(in)
			if err != nil {
				return err
			}
			if ans == "" {
				break
			}
			c := model.Category(ans)
			if !labelable(c) {
				say("  Pick one of: %s\n", strings.Join(choices, ", "))
				continue
			}
			if err := acts.label(a.App, c); err != nil {
				return err
			}
			say("  Saved: new time in %s counts as %s (undo: `burntout config remove classify.overrides %s=%s`).\n",
				printable(a.App), c, printable(a.App), c)
			break
		}
	}
	return nil
}

// readAnswer reads one trimmed line; q is errStop and the end of input
// io.EOF.
func readAnswer(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", io.EOF
	}
	line = strings.TrimSpace(line)
	if line == "q" || line == "quit" {
		return "", errStop
	}
	return line, nil
}

// splitNote splits an annotation typed on one line, keeping everything
// after --note as the note.
func splitNote(line string) []string {
	head, note, ok := strings.Cut(line, "--note")
	args := strings.Fields(head)
	if ok {
		args = append(args, "--note", strings.TrimSpace(note))
	}
	return args
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

// labelChoices are the categories an app can be given: not private (that
// is what exclusions are for) and not uncategorized.
func labelChoices() []string {
	var out []string
	for _, c := range model.Categories() {
		if labelable(c) {
			out = append(out, string(c))
		}
	}
	return out
}

func labelable(c model.Category) bool {
	return c.Valid() && c != model.CategoryPrivate && c != model.CategoryUncategorized
}

// unlabelled drops apps that already have an override: their past minutes
// stay uncategorized, and asking again every week would nag. An app id
// with "=" can't be written as KEY=CATEGORY, so it isn't asked about.
// Override keys are stored lower-cased and app ids keep their case
// (com.microsoft.VSCode), so the lookup lower-cases too.
func unlabelled(apps []sqlite.UncategorizedApp, cfg config.Config) []sqlite.UncategorizedApp {
	overrides := cfg.Overrides()
	var out []sqlite.UncategorizedApp
	for _, a := range apps {
		if _, ok := overrides[strings.ToLower(strings.TrimSpace(a.App))]; !ok && !strings.Contains(a.App, "=") {
			out = append(out, a)
		}
	}
	return out
}

// labelApp adds app=category to classify.overrides, the way `burntout
// config add` does. The daemon reloads the file.
func labelApp(path, app string, c model.Category) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("%w\nFix the file first (`burntout config validate`)", err)
	}
	if err := cfg.Add("classify.overrides", []string{app + "=" + string(c)}); err != nil {
		return err
	}
	return config.Save(path, cfg)
}
