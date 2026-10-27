package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/dashboard"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/export"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/fde"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/claude"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/shell"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/annotation"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// The privacy controls of privacy & security §5.1 that work from the
// command line: pause, forget, annotations and the privacy view.

// forgetWait bounds how long forget waits for the daemon.
const forgetWait = 30 * time.Second

// runPause pauses collection: for a duration, until tomorrow (the next
// logical day), or until resumed.
func runPause(_ context.Context, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: burnout pause [DURATION (e.g. 1h, 30m) | tomorrow]")
	}
	now := time.Now()
	var until time.Time
	if len(args) == 1 {
		var err error
		if until, err = pauseUntil(args[0], now, app.DefaultConfig().Rollover); err != nil {
			return err
		}
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	if err := paths.EnsurePrivateDir(dataDir); err != nil {
		return err
	}
	if err := control.SetPause(dataDir, until); err != nil {
		return err
	}
	fmt.Printf("Paused %s. Nothing is recorded until then; `burnout resume` ends it early.\n", describePause(control.Pause{Until: until}, now))
	return nil
}

// pauseUntil reads a pause length: a duration or "tomorrow", which is the
// start of the next logical day.
func pauseUntil(arg string, now time.Time, rollover time.Duration) (time.Time, error) {
	if arg == "tomorrow" {
		y, m, d := now.Add(-rollover).Date()
		return time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()).Add(rollover), nil
	}
	d, err := time.ParseDuration(arg)
	if err != nil || d <= 0 || d > 30*24*time.Hour {
		return time.Time{}, fmt.Errorf("pause for how long? %q isn't a duration like 1h or 30m (up to 30 days), or tomorrow", arg)
	}
	return now.Add(d), nil
}

func describePause(p control.Pause, now time.Time) string {
	switch {
	case p.Until.IsZero():
		return "until you resume"
	case p.Until.Sub(now) < 24*time.Hour && p.Until.YearDay() == now.YearDay():
		return "until " + p.Until.Format("15:04")
	default:
		return "until " + p.Until.Format("Mon 2 Jan 15:04")
	}
}

func runResume(context.Context, []string) error {
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	if paused, _ := control.PauseState(dataDir, time.Now()); !paused {
		fmt.Println("Not paused.")
		return control.Resume(dataDir) // clears an expired pause file
	}
	if err := control.Resume(dataDir); err != nil {
		return err
	}
	fmt.Println("Resumed.")
	return nil
}

// runForget deletes recorded data from a point on: the last hour (or any
// duration), today, since a date, or everything.
func runForget(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	last := fs.Duration("last", 0, "forget the last DURATION, e.g. 1h")
	today := fs.Bool("today", false, "forget everything recorded today")
	since := fs.String("since", "", "forget everything since a date, YYYY-MM-DD")
	all := fs.Bool("all", false, "forget everything recorded (annotations, boundaries, consent and hooks stay)")
	yes := fs.Bool("yes", false, "don't ask for confirmation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	from, what, err := forgetFrom(*last, *today, *since, *all, time.Now(), app.DefaultConfig().Rollover)
	if err != nil {
		return err
	}
	if !*yes {
		if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return errors.New("forget deletes data for good: run it in a terminal to confirm, or add --yes")
		}
		fmt.Printf("This deletes %s, including recovery copies made since then. It can't be undone.\nType \"yes\" to continue: ", what)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "yes" {
			fmt.Println("Nothing was deleted.")
			return nil
		}
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	if err := forgetData(ctx, dataDir, from); errors.Is(err, sqlite.ErrNoDatabase) {
		fmt.Println("Nothing is recorded yet.")
		return nil
	} else if err != nil {
		return err
	}
	fmt.Printf("Deleted %s.\n", what)
	if left, err := export.Existing(dataDir); err == nil && len(left) > 0 {
		fmt.Println("Exports you saved are still there; delete them yourself if they should go too:")
		for _, e := range left {
			fmt.Printf("  %s  (%s)\n", printable(e.Path), e.At.Format("2006-01-02 15:04"))
		}
	}
	return nil
}

// forgetData deletes everything recorded from from on, for forget and the
// dashboard. The daemon writes the database and holds unsaved data in
// memory, so while it runs it does the deleting. sqlite.ErrNoDatabase
// means nothing is recorded yet.
func forgetData(ctx context.Context, dataDir string, from time.Time) error {
	if daemonAnswers(ctx) {
		return forgetThroughDaemon(dataDir, from)
	}
	store, err := sqlite.OpenExisting(ctx, dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return store.Forget(ctx, from, model.LogicalDay(from, app.DefaultConfig().Rollover))
}

// forgetThroughDaemon asks the running daemon to forget everything from
// from on, and waits for it.
func forgetThroughDaemon(dataDir string, from time.Time) error {
	r, err := control.RequestForget(dataDir, from)
	if err != nil {
		return err
	}
	if err := control.WaitForgetDone(dataDir, r, forgetWait, 100*time.Millisecond); err != nil {
		control.TakeForget(dataDir) // withdraw it, so it doesn't run later unannounced
		return err
	}
	return nil
}

// forgetFrom turns the forget flags (exactly one) into a start time and a
// description.
func forgetFrom(last time.Duration, today bool, since string, all bool, now time.Time, rollover time.Duration) (time.Time, string, error) {
	set := 0
	for _, on := range []bool{last != 0, today, since != "", all} {
		if on {
			set++
		}
	}
	if set != 1 {
		return time.Time{}, "", errors.New("say what to forget: --last DURATION, --today, --since YYYY-MM-DD or --all")
	}
	switch {
	case last < 0:
		return time.Time{}, "", errors.New("--last must be positive")
	case last > 0:
		return now.Add(-last), "everything recorded in the last " + last.String(), nil
	case today:
		y, m, d := now.Add(-rollover).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Add(rollover), "everything recorded today", nil
	case since != "":
		t, err := time.ParseInLocation(time.DateOnly, since, now.Location())
		if err != nil {
			return time.Time{}, "", fmt.Errorf("--since %q: want YYYY-MM-DD", since)
		}
		return t.Add(rollover), "everything recorded since " + since, nil
	default:
		return time.UnixMilli(0), "everything recorded", nil
	}
}

// runAnnotate adds, lists and removes annotations.
func runAnnotate(ctx context.Context, args []string) error {
	const usage = "usage: burnout annotate KIND FROM [TO] [--note TEXT] | list | remove ID\n" +
		"  KIND: vacation, sick, on_call, release, exam or other; dates YYYY-MM-DD, today or yesterday"
	if len(args) == 0 {
		return errors.New(usage)
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
	today := model.LogicalDay(time.Now(), app.DefaultConfig().Rollover)

	switch args[0] {
	case "list":
		notes, err := store.Annotations(ctx, "0000-01-01", "9999-12-31")
		if err != nil {
			return err
		}
		if len(notes) == 0 {
			fmt.Println("No annotations. Mark time off with, for example, `burnout annotate vacation 2026-11-02 2026-11-06`.")
		}
		for _, a := range notes {
			fmt.Printf("  %3d  %s\n", a.ID, describeAnnotation(a))
		}
		return nil
	case "remove":
		if len(args) != 2 {
			return errors.New(usage)
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return errors.New(usage)
		}
		ok, err := store.RemoveAnnotation(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no annotation %d (see `burnout annotate list`)", id)
		}
		fmt.Println("Removed.")
		return nil
	}

	a, err := parseAnnotation(args, today)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	if _, err := store.AddAnnotation(ctx, a); err != nil {
		return err
	}
	fmt.Printf("Marked: %s\n", describeAnnotation(a))
	if a.Excludes() {
		fmt.Println("These days are left out of your baseline and get no strain score.")
	}
	return nil
}

// parseAnnotation reads KIND FROM [TO] [--note TEXT].
func parseAnnotation(args []string, today string) (annotation.Annotation, error) {
	var a annotation.Annotation
	var pos []string
	wantNote := false
	for _, arg := range args {
		switch {
		case wantNote:
			a.Note, wantNote = arg, false
		case arg == "--note" || arg == "-note":
			wantNote = true
		default:
			pos = append(pos, arg)
		}
	}
	if wantNote {
		return a, errors.New("--note needs text")
	}
	if len(pos) < 2 || len(pos) > 3 {
		return a, errors.New("give a kind and one or two dates")
	}
	a.Kind = annotation.Kind(strings.ReplaceAll(pos[0], "-", "_"))
	a.From = relativeDay(pos[1], today)
	a.To = a.From
	if len(pos) == 3 {
		a.To = relativeDay(pos[2], today)
	}
	return a, a.Validate()
}

func relativeDay(s, today string) string {
	t, _ := time.Parse(time.DateOnly, today)
	switch s {
	case "today":
		return today
	case "yesterday":
		return t.AddDate(0, 0, -1).Format(time.DateOnly)
	case "tomorrow":
		return t.AddDate(0, 0, 1).Format(time.DateOnly)
	}
	return s
}

func describeAnnotation(a annotation.Annotation) string {
	s := strings.ReplaceAll(string(a.Kind), "_", "-") + ", " + a.From
	if a.To != a.From {
		s += fmt.Sprintf(" to %s (%d days)", a.To, a.Days())
	}
	if a.Note != "" {
		s += ": " + printable(a.Note)
	}
	return s
}

// runPrivacy shows what is collected and the user's current settings
// (privacy & security §5.1 "Privacy view").
func runPrivacy(ctx context.Context, _ []string) error {
	fmt.Print(summary(titlesOn()))
	fmt.Println("\nYour settings:")
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	for _, row := range privacySettings(ctx, dataDir) {
		fmt.Printf("  %-18s%s\n", row.Name+":", row.Value)
	}
	fmt.Print(`
Your controls:
  burnout pause [1h | tomorrow]     stop recording for a while (or until resume)
  burnout resume
  burnout forget --last 1h | --today | --since YYYY-MM-DD | --all
  burnout annotate vacation FROM TO  mark time off; such days don't count
  burnout config set privacy.private_hours "Mon-Fri 19:00-08:00" "Sat,Sun"
  burnout config add privacy.exclude_apps APP   record an app as private
  burnout config set privacy.aggregate_only true
  burnout config set retention.minutes_days 30  keep less (all keys: burnout config get)
  burnout export --out FILE [--format csv]       your data as JSON or CSV
  burnout uninstall [--purge]        remove hooks and the service (--purge: and all your data)
Details: docs/privacy-and-security.md
`)
	return nil
}

// privacySettings are the user's current privacy settings, for
// `burnout privacy` and the dashboard's Privacy page (privacy & security
// §5.1 "Privacy view"). Values are single lines, printable.
func privacySettings(ctx context.Context, dataDir string) []dashboard.Setting {
	var rows []dashboard.Setting
	add := func(name, value string) { rows = append(rows, dashboard.Setting{Name: name, Value: printable(value)}) }
	now := time.Now()
	switch consentStatus() {
	case consentCurrent:
		add("Consent", "given (`burnout setup`)")
	case consentStale:
		add("Consent", "out of date: what is collected changed; nothing is collected until you run `burnout setup` again")
	default:
		add("Consent", "not given; nothing is collected")
	}
	cfg, cfgPath, cfgErr := loadConfig()
	if cfgErr != nil {
		add("Config file", "INVALID, see `burnout config validate`; showing the defaults")
		cfg = config.Default()
	} else {
		add("Config file", cfgPath)
	}
	add("Profile", fmt.Sprintf("%s, core hours %s", cfg.Profile.Role, cfg.Profile.CoreHours))
	collecting := "not running"
	running := daemonAnswers(ctx)
	if running {
		collecting = "running"
	}
	if paused, p := control.PauseState(dataDir, now); paused {
		collecting += ", paused " + describePause(p, now)
	} else if w, ok := privateNow(cfg, now); ok {
		collecting += ", private hours now (" + w + ")"
	}
	add("Collection", collecting)
	add("Pause", "the tray menu, the dashboard's Privacy page, or `burnout pause 1h` / `burnout pause tomorrow`; `burnout resume` to start again")
	add("Change settings", "`burnout config set KEY VALUE` (all keys: `burnout config get`), or edit the config file above")
	add("Uninstall", "`burnout uninstall`; add `--purge` to delete all your data too")
	add("Window titles", titlesLine(cfg, daemonReadsTitles(running, dataDir)))
	add("Keystroke counts", "off (not collected)")
	hours := "none"
	if len(cfg.Privacy.PrivateHours) > 0 {
		hours = strings.Join(cfg.Privacy.PrivateHours, "; ")
	}
	add("Private hours", hours)
	excluded := ""
	if n := len(cfg.Privacy.ExcludeApps); n > 0 {
		excluded = fmt.Sprintf(" and %d you excluded", n)
	}
	add("Always private", fmt.Sprintf("%d sensitive apps (password managers, finance, crypto wallets, health)%s; "+
		"their time counts, their names aren't kept", len(classify.SensitiveApps()), excluded))
	if cfg.Privacy.TitleClassification {
		sites := ""
		if n := len(cfg.Privacy.ExcludeDomains); n > 0 {
			sites = fmt.Sprintf(" and %d you excluded", n)
		}
		add("Private sites", fmt.Sprintf("%d sensitive sites (banking, payments, health, dating, password vaults)%s; "+
			"their time counts as private", len(classify.SensitiveDomains()), sites))
	}
	if n := len(cfg.Classify.Overrides); n > 0 {
		add("Your categories", fmt.Sprintf("%d apps or sites (`burnout config get classify.overrides`)", n))
	}
	if cfg.Privacy.AggregateOnly {
		add("Aggregate-only", "on: time per category only, no app names or per-window detail")
	} else {
		add("Aggregate-only", "off: app names are kept for each minute")
	}
	add("Hooks", hookSummary())
	add("Calendar", calendarLine(dataDir))
	add("Network", "calendar feed: "+calendarNetwork(dataDir)+"; update check: "+updateLine(dataDir, cfg)+"; model download: not built")
	state, _, _ := fde.Status(ctx, filepath.Dir(dataDir))
	add("Data folder", fmt.Sprintf("%s (owner-only; disk encryption %s)", dataDir, state))
	exports := "none on disk"
	if left, err := export.Existing(dataDir); err == nil && len(left) > 0 {
		exports = fmt.Sprintf("%d still on disk (plaintext), latest %s", len(left), left[len(left)-1].Path)
	}
	add("Exports", exports)
	r := cfg.Retention
	add("Kept for", fmt.Sprintf("raw events %s, minutes %s, intervals %s, features and scores %s, check-ins %s",
		days(r.RawEventsDays), days(r.MinutesDays), days(r.IntervalsDays), days(r.DailyDays), days(r.CheckinsDays)))
	return rows
}

// hookSummary lists which hooks are installed.
func hookSummary() string {
	var on, off []string
	for _, k := range []shell.Kind{shell.Zsh, shell.Bash, shell.Fish} {
		if o, err := shellOptions(k, ""); err == nil && shell.Installed(o) {
			on = append(on, string(k))
		} else {
			off = append(off, string(k))
		}
	}
	if path, err := claude.DefaultSettingsPath(); err == nil {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), "hook claude") { //nolint:gosec // the user's own settings
			on = append(on, "Claude Code")
		} else {
			off = append(off, "Claude Code")
		}
	}
	s := "none"
	if len(on) > 0 {
		s = strings.Join(on, ", ")
	}
	if len(off) > 0 {
		s += " (not: " + strings.Join(off, ", ") + ")"
	}
	return s
}

// titlesOn reports whether title classification is on in a valid config.
func titlesOn() bool {
	c, _, err := loadConfig()
	return err == nil && c.Privacy.TitleClassification
}

// daemonReadsTitles reports whether a daemon is running whose desktop
// collector reads window titles. It decided that when it started, so it
// can disagree with the config file until it restarts.
func daemonReadsTitles(running bool, dataDir string) bool {
	return running && control.DaemonTitles(dataDir)
}

// titlesLine describes title classification, for privacy and doctor.
// daemonTitles is daemonReadsTitles.
func titlesLine(c config.Config, daemonTitles bool) string {
	if !c.Privacy.TitleClassification && daemonTitles {
		return "off from the next daemon start: the running daemon still reads browser titles in memory, never stored; " +
			"`burnout service install` restarts it now"
	}
	if c.Privacy.TitleClassification {
		s := "on: browser titles are redacted and classified in memory, never stored; only the category is kept"
		if runtime.GOOS == "darwin" {
			s += " (needs the Screen Recording permission)"
		}
		return s
	}
	s := "off (not collected); `burnout config set privacy.title_classification true` tells sites apart in the browser"
	if runtime.GOOS == "darwin" {
		s += " (it needs the Screen Recording permission)"
	}
	return s
}
