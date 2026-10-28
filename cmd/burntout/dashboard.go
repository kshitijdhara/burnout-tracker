package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/control"
	"github.com/kshitijdhara/burntout/internal/adapters/dashboard"
	"github.com/kshitijdhara/burntout/internal/adapters/fde"
	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/adapters/portal"
	"github.com/kshitijdhara/burntout/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burntout/internal/app"
	"github.com/kshitijdhara/burntout/internal/config"
	"github.com/kshitijdhara/burntout/internal/domain/annotation"
	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/checkin"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/strain"
)

// dashboardDays is how much history the overview shows.
const dashboardDays = 90

// runDashboard serves the local dashboard until interrupted and opens it
// in the browser with a one-time link (privacy & security §8.2).
func runDashboard(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	noOpen := fs.Bool("no-open", false, "print the one-time link instead of opening the browser")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		return errors.New("nothing recorded yet: run `burntout setup`, then `burntout service install`")
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	srv, err := dashboard.Listen(dashSource{store: store, dataDir: dataDir}, dashControl{dataDir: dataDir}, log)
	if err != nil {
		return err
	}
	link := srv.LaunchURL()
	opened := false
	if !*noOpen {
		opened = openLaunch(dataDir, link) == nil
	}
	if opened {
		fmt.Println("The dashboard is open in your browser.")
	} else {
		fmt.Printf("Open this one-time link within %d seconds:\n  %s\n", int(dashboard.TokenTTL.Seconds()), link)
	}
	fmt.Println("It shows data from this device only and stops when you press Ctrl-C.")
	return srv.Serve(ctx)
}

// openLaunch opens a dashboard launch link in the default browser without
// putting it on a command line, where other local users could read the
// token (privacy & security §2, T1): the link goes into an owner-only
// launch file in the data folder, and the browser opens that file. The
// file is removed once the token has expired, or by the next launch if
// BurntOut stops first.
func openLaunch(dataDir, link string) error {
	path, err := dashboard.WriteLaunchFile(dataDir, link, time.Now())
	if err != nil {
		return err
	}
	if err := openFile(path); err != nil {
		_ = os.Remove(path)
		return err
	}
	time.AfterFunc(dashboard.LaunchFileTTL, func() { _ = os.Remove(path) })
	return nil
}

// openFile opens a launch file; tests replace it.
var openFile = openLaunchFile

// openLaunchFile opens a launch file in the browser. On Linux it asks the
// desktop portal first: a snap or flatpak browser (Ubuntu's Firefox is a
// snap) can't read files under ~/.local/share, but can read one the portal
// hands it. Without a portal, or if it refuses, xdg-open opens the path.
func openLaunchFile(path string) error {
	if runtime.GOOS == "linux" {
		if err := portal.OpenFile(context.Background(), path); err == nil {
			return nil
		}
	}
	return openBrowser(path)
}

// openBrowser opens target, a URL or a file, in the default browser or
// app. It is ours: a launch file's path, or a fixed URL, no user input.
func openBrowser(target string) error {
	cmd := browserCommand(target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // the daemon opens it too, and must reap it
	return nil
}

// browserCommand is the command openBrowser runs.
func browserCommand(target string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("/usr/bin/open", target) //nolint:gosec // see openBrowser
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target) //nolint:gosec // see openBrowser
	default:
		return exec.Command("xdg-open", target) //nolint:gosec // see openBrowser
	}
}

// collected is what is and isn't collected, for the Privacy page. titles
// says whether title classification is on.
func collected(titles bool) []string {
	window := "Which apps and windows hold your attention, per minute. Window titles are not collected."
	if titles {
		window = "Which apps and windows hold your attention, per minute. Browser titles are read to tell sites apart: " +
			"redacted and classified in memory, never stored; only the category is kept."
	}
	return append([]string{window}, collectedRest...)
}

var collectedRest = []string{
	"Whether you are present: idle time, screen lock, calls and visible video.",
	"Shell commands: the command name and a known subcommand only, never arguments, plus exit code, duration and a keyed hash of the project.",
	"Claude Code session timing: when it works, waits for you and stops. Never your prompts or its responses.",
	"Calendar meetings, if you added a feed: start and end times only, never titles, attendees or places.",
	"Check-in answers, annotations and boundaries you enter.",
	"Nothing leaves this device. There is no server and no telemetry.",
}

// dashSource reads the dashboard's views from the database, read-only,
// with the same code as status, review and privacy.
type dashSource struct {
	store   *sqlite.Store
	dataDir string
}

// config is the current config; an invalid file gives the defaults and a
// notice.
func (d dashSource) config() (config.Config, []string) {
	c, _, err := loadConfig()
	if err != nil {
		return config.Default(), []string{"Your config file is invalid, so the default profile is shown. Run `burntout config validate`."}
	}
	return c, nil
}

func (d dashSource) Overview(ctx context.Context) (dashboard.Overview, error) {
	now := time.Now()
	cfg, notices := d.config()
	opts := scoreOptions(cfg)
	today := model.LogicalDay(now, opts.Rollover)
	sc, err := app.ScoreDays(ctx, d.store, now, opts)
	if err != nil {
		return dashboard.Overview{}, err
	}
	t, _ := time.Parse(time.DateOnly, today)
	from := t.AddDate(0, 0, -dashboardDays).Format(time.DateOnly)
	notes, err := d.store.Annotations(ctx, from, today)
	if err != nil {
		return dashboard.Overview{}, err
	}
	o := dashboard.Overview{
		Today:   today,
		Profile: fmt.Sprintf("%s, core hours %s", cfg.Profile.Role, cfg.Profile.CoreHours),
		Days:    []dashboard.DayScore{},
		Notices: append(notices, d.notices(ctx, cfg, now)...),
	}
	for _, s := range sc.Scores {
		if s.Date >= from {
			o.Days = append(o.Days, dayScore(s, notes))
		}
	}
	if n := len(sc.Scores); n > 0 {
		latest := dayScore(sc.Scores[n-1], notes)
		o.Latest = &latest
		if !slices.ContainsFunc(sc.Scores, func(s strain.Score) bool { return s.Status == strain.StatusOK }) {
			o.Calibrating = &dashboard.Calibration{UsableDays: sc.Scores[n-1].UsableDays, Needed: strain.DefaultParams().CalibrationDays}
		}
	}
	return o, nil
}

// notices are what the user should know now.
func (d dashSource) notices(ctx context.Context, cfg config.Config, now time.Time) []string {
	var out []string
	if paused, p := control.PauseState(d.dataDir, now); paused {
		out = append(out, "Recording is paused "+describePause(p, now)+". `burntout resume` starts it again.")
	} else if w, ok := privateNow(cfg, now); ok {
		out = append(out, "Private hours now ("+w+"): nothing is being recorded.")
	}
	if !daemonAnswers(ctx) {
		out = append(out, "The daemon isn't running, so nothing new is recorded. `burntout service install` starts it at login.")
	}
	if state, _, _ := fde.Status(ctx, d.dataDir); state == fde.Disabled {
		out = append(out, "Disk encryption is off: your data is unprotected if this device is lost or stolen. Turn on FileVault, BitLocker or LUKS.")
	}
	return out
}

func dayScore(s strain.Score, notes []annotation.Annotation) dashboard.DayScore {
	d := dashboard.DayScore{Date: s.Date, Status: string(s.Status), Reason: s.Reason}
	if s.Status == strain.StatusOK {
		v := s.Strain
		d.Strain, d.Band = &v, strainBand(v)
		for _, dr := range s.Drivers {
			d.Drivers = append(d.Drivers, dr.Text())
		}
	}
	for _, g := range s.Guardrails {
		d.Guardrails = append(d.Guardrails, g.Text)
	}
	for _, a := range notes {
		if a.Covers(s.Date) {
			d.Marked = describeAnnotation(a)
			break
		}
	}
	return d
}

func (d dashSource) Day(ctx context.Context, date string) (dashboard.Day, error) {
	if date == "" {
		date = model.LogicalDay(time.Now(), rollover)
	}
	sum, err := d.store.DaySummary(ctx, date)
	if err != nil {
		return dashboard.Day{}, err
	}
	in, err := d.store.DayInput(ctx, date)
	if err != nil {
		return dashboard.Day{}, err
	}
	out := dashboard.Day{
		Date: date, Unattended: sum.UnattributedSeconds,
		Commands: sum.ShellCommands, Failures: sum.ShellFailures, Agents: sum.AgentSessions,
		Categories: named(sum.CategorySeconds), AgentStates: named(sum.AgentStateSeconds), HumanStates: named(sum.HumanStateSeconds),
		TopApps: []dashboard.Named{}, TopCommands: []dashboard.Named{}, Meetings: []dashboard.Meeting{}, Timeline: []dashboard.SlotEntry{},
	}
	for _, c := range out.Categories {
		out.AttendedS += c.Value
	}
	for _, a := range sum.TopApps {
		out.TopApps = append(out.TopApps, dashboard.Named{Name: printable(a.Name), Value: a.Seconds})
	}
	for _, c := range sum.TopCommands {
		out.TopCommands = append(out.TopCommands, dashboard.Named{Name: printable(c.Name), Value: float64(c.Count)})
	}
	for _, b := range in.Busy {
		out.Meetings = append(out.Meetings, dashboard.Meeting{
			Start: b.Start.Format("15:04"), End: b.End.Format("15:04"), Tentative: b.State == "tentative",
		})
	}
	// Ten-minute slots, each labelled with the category that held most
	// attention in it.
	type slot struct {
		start time.Time
		cats  map[string]float64
	}
	var slots []*slot
	for _, m := range in.Minutes {
		start := m.At.Truncate(10 * time.Minute)
		if n := len(slots); n == 0 || !slots[n-1].start.Equal(start) {
			slots = append(slots, &slot{start: start, cats: map[string]float64{}})
		}
		slots[len(slots)-1].cats[string(m.Category)] += m.AttendedS
	}
	for _, s := range slots {
		best, total := "", 0.0
		for _, c := range slices.Sorted(maps.Keys(s.cats)) {
			total += s.cats[c]
			if best == "" || s.cats[c] > s.cats[best] {
				best = c
			}
		}
		if total > 0 {
			out.Timeline = append(out.Timeline, dashboard.SlotEntry{Start: s.start.Format("15:04"), Category: best, AttendedS: total})
		}
	}
	return out, nil
}

// named turns seconds by name into a list, largest first.
func named[K ~string](m map[K]float64) []dashboard.Named {
	out := make([]dashboard.Named, 0, len(m))
	for k, v := range m {
		if v > 0 {
			out = append(out, dashboard.Named{Name: string(k), Value: v})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (d dashSource) Week(ctx context.Context) (dashboard.Week, error) {
	cfg, _ := d.config()
	w, due, err := weekReview(ctx, d.store, cfg, time.Now())
	if err != nil {
		return dashboard.Week{}, err
	}
	out := dashboard.Week{
		From: w.From, To: w.To, Calibrating: w.Calibrating, Scored: w.Scored, CheckinDue: due,
		Drivers: []dashboard.Count{}, Limits: []dashboard.Count{}, Checkins: []dashboard.Check{},
		Marked: []string{}, Suggestions: w.Suggestions, Adherence: []dashboard.Adherence{}, Offers: []dashboard.Offer{},
	}
	if out.Suggestions == nil {
		out.Suggestions = []string{}
	}
	if !w.Calibrating {
		avg := w.Average
		out.Average = &avg
		out.Peak = &dashboard.Peak{Date: w.Peak.Date, Strain: w.Peak.Strain, Band: strainBand(w.Peak.Strain)}
		if w.PrevScored > 0 {
			prev := w.PrevAverage
			out.PrevAverage = &prev
		}
	}
	for _, r := range w.Drivers[:min(5, len(w.Drivers))] {
		out.Drivers = append(out.Drivers, dashboard.Count{Label: r.Label, Days: r.Days})
	}
	for _, g := range w.Guardrails {
		out.Limits = append(out.Limits, dashboard.Count{Label: g.Label, Days: g.Days})
	}
	for _, id := range []string{checkin.CBIWorkID, checkin.CBIPersonalID} {
		r, ok := w.Checkin[id]
		if !ok {
			continue
		}
		inst, _ := checkin.ByID(id)
		c := dashboard.Check{Name: inst.Name, Score: r.Score, Band: string(checkin.BandOf(r.Score))}
		if p, ok := w.PrevCheckin[id]; ok {
			prev := p.Score
			c.Previous = &prev
		}
		out.Checkins = append(out.Checkins, c)
	}
	for _, a := range w.Annotations {
		out.Marked = append(out.Marked, describeAnnotation(a))
	}
	for _, a := range w.Adherence {
		missed := a.Missed
		if missed == nil {
			missed = []string{}
		}
		out.Adherence = append(out.Adherence, dashboard.Adherence{
			Text: a.Boundary.Describe(), Summary: describeAdherence(a, w.To), Kept: a.Kept, Judged: a.Judged, Missed: missed,
		})
	}
	for _, o := range w.Offers {
		out.Offers = append(out.Offers, dashboard.Offer{
			Kind: string(o.Boundary.Kind), Value: o.Boundary.Value, Text: o.Boundary.Describe(), For: o.For,
		})
	}
	return out, nil
}

func (d dashSource) Privacy(ctx context.Context) (dashboard.Privacy, error) {
	return dashboard.Privacy{Collected: collected(titlesOn()), Settings: privacySettings(ctx, d.dataDir)}, nil
}

// dashControl makes the dashboard's changes the way the CLI does, so a
// button and its command can't disagree: pause, resume and forget through
// the control files (and forgetData), annotations and check-ins through a
// short-lived read-write connection, as `burntout annotate` and `burntout
// checkin` use while the daemon runs. Both the CLI's dashboard and the
// tray's get one.
type dashControl struct{ dataDir string }

func (d dashControl) Pause(_ context.Context, length string) error {
	until := pauseEnd(length == dashboard.PauseHour, length == dashboard.PauseTomorrow, time.Now())
	return control.SetPause(d.dataDir, until)
}

func (d dashControl) Resume(context.Context) error { return control.Resume(d.dataDir) }

func (d dashControl) Forget(ctx context.Context, span string) error {
	from, err := recentStart(span == dashboard.ForgetHour, time.Now())
	if err != nil {
		return err
	}
	if err := forgetData(ctx, d.dataDir, from); !errors.Is(err, sqlite.ErrNoDatabase) {
		return err
	}
	return nil
}

func (d dashControl) Annotations(ctx context.Context) ([]dashboard.Annotation, error) {
	var out []dashboard.Annotation
	err := rwStore(d).with(ctx, func(s *sqlite.Store) error {
		notes, err := s.Annotations(ctx, "0000-01-01", "9999-12-31")
		for _, a := range notes {
			out = append(out, dashboard.Annotation{ID: a.ID, Text: describeAnnotation(a)})
		}
		return err
	})
	return out, err
}

func (d dashControl) AddAnnotation(ctx context.Context, a annotation.Annotation) (id int64, err error) {
	err = rwStore(d).with(ctx, func(s *sqlite.Store) error {
		id, err = s.AddAnnotation(ctx, a)
		return err
	})
	return id, err
}

func (d dashControl) RemoveAnnotation(ctx context.Context, id int64) (ok bool, err error) {
	err = rwStore(d).with(ctx, func(s *sqlite.Store) error {
		ok, err = s.RemoveAnnotation(ctx, id)
		return err
	})
	return ok, err
}

func (d dashControl) SaveCheckins(ctx context.Context, rs []checkin.Record) error {
	return rwStore(d).with(ctx, func(s *sqlite.Store) error { return s.SaveCheckins(ctx, rs) })
}

func (d dashControl) Boundaries(ctx context.Context) ([]dashboard.Boundary, error) {
	bs, err := rwStore(d).Boundaries(ctx)
	var out []dashboard.Boundary
	for _, b := range boundary.Current(bs) {
		out = append(out, dashboard.Boundary{ID: b.ID, Kind: string(b.Kind), Value: b.Value, Text: b.Describe(), Since: b.Since})
	}
	return out, err
}

func (d dashControl) SetBoundary(ctx context.Context, b boundary.Boundary) (int64, bool, error) {
	now := time.Now()
	return rwStore(d).SetBoundary(ctx, b, model.LogicalDay(now, rollover), now)
}

func (d dashControl) RemoveBoundary(ctx context.Context, id int64) (bool, error) {
	now := time.Now()
	return rwStore(d).RemoveBoundary(ctx, id, model.LogicalDay(now, rollover), now)
}

func (dashControl) Region() string { return regionFromEnv() }
