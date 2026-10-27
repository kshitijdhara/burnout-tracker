package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/egress/ics"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/calendar"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/schedule"
)

// icsKey is the keystore entry holding the calendar feed URL or path.
const icsKey = "ics-url"

// Calendar sync timing: how far back a sync replaces busy blocks, how far
// ahead it stores them, and how often the daemon syncs.
const (
	calendarBack     = 60 * 24 * time.Hour
	calendarAhead    = 2 * 24 * time.Hour
	calendarInterval = 3 * time.Hour
	calendarFirst    = time.Minute
)

// calendarStatusFile, in the data folder, records the last sync's outcome
// (never the URL).
const calendarStatusFile = "calendar-sync.json"

type calendarStatus struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"` // host or file name
	OK     bool      `json:"ok"`
	Blocks int       `json:"blocks,omitempty"`
	Error  string    `json:"error,omitempty"`
}

const calendarUsage = `usage: burnout calendar COMMAND
  add URL|FILE [--yes]   use an ICS feed (https:// or webcal://) or an .ics file
  sync                   fetch it now
  status                 where it comes from and how the last sync went
  remove [--forget]      stop using it; --forget also deletes the meetings stored from it`

func runCalendar(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(calendarUsage)
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	if args[0] == "status" {
		return calendarStatusCmd(dataDir)
	}
	keys, err := keystore.New(dataDir, keystoreOptions()...)
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		return calendarAdd(ctx, dataDir, keys, args[1:])
	case "sync":
		return calendarSyncNow(ctx, dataDir, keys)
	case "remove":
		return calendarRemove(ctx, dataDir, keys, args[1:])
	}
	return errors.New(calendarUsage)
}

func calendarAdd(ctx context.Context, dataDir string, keys *keystore.Store, args []string) error {
	fs := flag.NewFlagSet("calendar add", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "don't ask for confirmation")
	var pos []string
	for len(args) > 0 { // flags may come after the URL
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
	if len(pos) != 1 {
		return errors.New(calendarUsage)
	}
	src, err := ics.Parse(model.NewSensitive(pos[0]))
	if err != nil {
		return err
	}
	if src.Remote() && !*yes {
		if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			explainCalendarURL(os.Stdout, src)
			return errors.New("run this in a terminal to confirm, or add --yes")
		}
		if !confirmCalendarURL(bufio.NewReader(os.Stdin), os.Stdout, src) {
			return nil
		}
	}
	return addCalendarSource(ctx, dataDir, keys, src)
}

func explainCalendarURL(out io.Writer, src ics.Source) {
	_, _ = fmt.Fprintf(out, `This URL is a key to your whole calendar. burnout keeps it in your OS keystore,
never in files or logs, and fetches it over HTTPS from %s about every 3 hours.
Only meeting times are kept: no titles, attendees, locations or notes.
`, src.Describe())
}

// confirmCalendarURL explains that a remote feed's URL is a secret and
// asks for a typed "yes".
func confirmCalendarURL(in *bufio.Reader, out io.Writer, src ics.Source) bool {
	explainCalendarURL(out, src)
	_, _ = fmt.Fprint(out, "Type \"yes\" to continue: ")
	line, err := in.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "yes" {
		_, _ = fmt.Fprintln(out, "Not added.")
		return false
	}
	return true
}

// addCalendarSource checks the feed works, keeps it and syncs it.
func addCalendarSource(ctx context.Context, dataDir string, keys *keystore.Store, src ics.Source) error {
	now := time.Now()
	blocks, err := fetchBlocks(ctx, src, now.Add(-14*24*time.Hour), now)
	if err != nil {
		return err
	}
	if err := keys.Put(icsKey, src.Value()); err != nil {
		return err
	}
	c, path, err := loadConfig()
	if err == nil && !c.Calendar.ICS.Enabled {
		c.Calendar.ICS.Enabled = true
		if err := config.Save(path, c); err != nil {
			return err
		}
	}
	fmt.Printf("Added %s: %d meetings in the last two weeks.\n", printable(src.Describe()), len(blocks))
	n, err := syncCalendar(ctx, dataDir, src, now)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		fmt.Println("The daemon fetches it once it runs (`burnout service install`).")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("Stored %d meetings from the last %d days. The daemon keeps them up to date.\n", n, int(calendarBack.Hours()/24))
	return nil
}

func calendarSyncNow(ctx context.Context, dataDir string, keys *keystore.Store) error {
	src, err := calendarSource(keys)
	if err != nil {
		return err
	}
	if c, _, err := loadConfig(); err == nil && !c.Calendar.ICS.Enabled {
		return errors.New("calendar fetching is off: `burnout config set calendar.ics.enabled true` turns it on")
	}
	n, err := syncCalendar(ctx, dataDir, src, time.Now())
	if errors.Is(err, sqlite.ErrNoDatabase) {
		return errors.New("nothing recorded yet: start the daemon first")
	}
	if err != nil {
		return err
	}
	fmt.Printf("Synced %s: %d meetings stored.\n", printable(src.Describe()), n)
	return nil
}

func calendarStatusCmd(dataDir string) error {
	src, err := viewSource(dataDir)
	if err != nil {
		fmt.Println("No calendar. Add one with `burnout calendar add URL`.")
		return nil //nolint:nilerr // "no calendar" is a status, not a failure
	}
	fmt.Println(describeCalendar(dataDir, src))
	return nil
}

// describeCalendar is the one-line calendar status for status, doctor and
// privacy.
func describeCalendar(dataDir string, src ics.Source) string {
	s := "from " + printable(src.Describe())
	if c, _, err := loadConfig(); err == nil && !c.Calendar.ICS.Enabled {
		s += ", fetching off (calendar.ics.enabled = false)"
	}
	st, ok := readCalendarStatus(dataDir)
	switch {
	case !ok:
		s += ", not synced yet"
	case st.OK:
		s += fmt.Sprintf(", last synced %s (%d meetings)", st.At.Format("Jan 2 15:04"), st.Blocks)
	default:
		s += fmt.Sprintf(", last sync failed %s: %s", st.At.Format("Jan 2 15:04"), printable(st.Error))
	}
	return s
}

func calendarRemove(ctx context.Context, dataDir string, keys *keystore.Store, args []string) error {
	fs := flag.NewFlagSet("calendar remove", flag.ContinueOnError)
	forget := fs.Bool("forget", false, "also delete the meetings stored from the calendar")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := keys.Delete(icsKey); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dataDir, calendarStatusFile))
	if !*forget {
		fmt.Println("Removed. Meetings already stored stay until their retention ends; `burnout calendar remove --forget` deletes them now.")
		return nil
	}
	store, err := sqlite.OpenExisting(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		fmt.Println("Removed.")
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if err := store.RemoveCalendar(ctx); err != nil {
		return err
	}
	fmt.Println("Removed, and the meetings stored from it are deleted.")
	return nil
}

// viewKeys opens the keystore for a read-only view, without setting one up
// (which may prompt): no store yet means no calendar.
func viewKeys(dataDir string) (*keystore.Store, error) {
	if len(keystoreOptions()) == 0 && !keystore.Exists(dataDir) {
		return nil, errors.New("no keystore yet")
	}
	return keystore.New(dataDir, keystoreOptions()...)
}

// calendarLine is the calendar's one-line summary, for privacy.
func calendarLine(dataDir string) string {
	src, err := viewSource(dataDir)
	if err != nil {
		return "none (`burnout calendar add URL` adds one; only meeting times are kept)"
	}
	return describeCalendar(dataDir, src)
}

// viewSource is the calendar source for read-only views.
func viewSource(dataDir string) (ics.Source, error) {
	keys, err := viewKeys(dataDir)
	if err != nil {
		return ics.Source{}, err
	}
	return calendarSource(keys)
}

// networkSummary says which outbound calls are configured, for doctor.
func networkSummary(dataDir string, c config.Config) string {
	return "Network:\n  calendar feed: " + calendarNetwork(dataDir) +
		"\n  update check: " + updateLine(dataDir, c) +
		"\n  model download: not built"
}

// calendarNetwork says whether the calendar uses the network.
func calendarNetwork(dataDir string) string {
	src, err := viewSource(dataDir)
	switch {
	case err != nil:
		return "none added"
	case !src.Remote():
		return "none (a local file: " + describeCalendar(dataDir, src) + ")"
	}
	return describeCalendar(dataDir, src)
}

// calendarSource reads the feed location from the keystore.
func calendarSource(keys *keystore.Store) (ics.Source, error) {
	v, ok, err := keys.Get(icsKey)
	if err != nil {
		return ics.Source{}, err
	}
	if !ok {
		return ics.Source{}, errors.New("no calendar: add one with `burnout calendar add URL`")
	}
	return ics.Parse(v)
}

// fetchBlocks fetches and parses the feed's busy blocks in [from, to).
func fetchBlocks(ctx context.Context, src ics.Source, from, to time.Time) ([]calendar.Block, error) {
	ctx, cancel := context.WithTimeout(ctx, ics.Timeout)
	defer cancel()
	r, err := ics.Open(ctx, src, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	blocks, err := calendar.Parse(r, calendar.Options{From: from, To: to, Zone: loadZone, Local: time.Local})
	if err != nil {
		return nil, err
	}
	return blocks, nil
}

// loadZone resolves a TZID: an IANA name, or a Windows one Outlook uses.
func loadZone(tzid string) (*time.Location, error) {
	if z, err := time.LoadLocation(tzid); err == nil {
		return z, nil
	}
	if iana, ok := calendar.WindowsZone(tzid); ok {
		return time.LoadLocation(iana)
	}
	return nil, fmt.Errorf("unknown time zone %q", tzid)
}

// syncCalendar fetches the feed and replaces the stored busy blocks of the
// last calendarBack and the next calendarAhead. It records the outcome.
func syncCalendar(ctx context.Context, dataDir string, src ics.Source, now time.Time) (int, error) {
	store, err := sqlite.OpenExisting(ctx, dataDir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = store.Close() }()
	return syncInto(ctx, store, dataDir, src, now, profileConfig().PrivateHours())
}

// syncInto is syncCalendar with an open store (the daemon's). Meetings
// starting in private hours are left out: private hours record nothing.
func syncInto(ctx context.Context, store *sqlite.Store, dataDir string, src ics.Source, now time.Time, private schedule.Schedule) (int, error) {
	from, to := now.Add(-calendarBack), now.Add(calendarAhead)
	n, err := func() (int, error) {
		blocks, err := fetchBlocks(ctx, src, from, to)
		if err != nil {
			return 0, err
		}
		busy := make([]sqlite.BusyBlock, 0, len(blocks))
		for _, b := range blocks {
			start := b.Start.In(time.Local)
			if private.Contains(start) {
				continue
			}
			busy = append(busy, sqlite.BusyBlock{Start: start, End: b.End.In(time.Local), Tentative: b.Tentative})
		}
		// Every logical day the sync covered, up to today: a day in it
		// without meetings had none, rather than no calendar.
		var days []string
		for d := from; !d.After(now); d = d.Add(24 * time.Hour) {
			days = append(days, model.LogicalDay(d, rollover))
		}
		return store.ReplaceBusy(ctx, from, to, busy, days, rollover)
	}()
	st := calendarStatus{At: now, Source: src.Describe(), OK: err == nil, Blocks: n}
	if err != nil {
		st.Error = err.Error()
	}
	writeCalendarStatus(dataDir, st)
	return n, err
}

// calendarLoop keeps the calendar up to date from the daemon: first a
// minute after start, then every calendarInterval. It doesn't fetch while
// collection is paused or the feed is switched off.
func calendarLoop(ctx context.Context, log *slog.Logger, store *sqlite.Store, dataDir string, keys *keystore.Store, live *liveConfig) {
	wait := calendarFirst
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = calendarInterval
		if !live.current().Calendar.ICS.Enabled {
			continue
		}
		if paused, _ := control.PauseState(dataDir, time.Now()); paused {
			continue
		}
		src, err := calendarSource(keys)
		if err != nil {
			continue // no calendar added
		}
		c := live.current()
		if n, err := syncInto(ctx, store, dataDir, src, time.Now(), c.PrivateHours()); err != nil {
			log.Warn("calendar sync failed", "source", src.Describe(), "err", err)
		} else {
			log.Info("calendar synced", "meetings", n)
		}
	}
}

func writeCalendarStatus(dataDir string, st calendarStatus) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	path := filepath.Join(dataDir, calendarStatusFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func readCalendarStatus(dataDir string) (calendarStatus, bool) {
	f, err := os.Open(filepath.Join(dataDir, calendarStatusFile)) //nolint:gosec // our own data folder
	if err != nil {
		return calendarStatus{}, false
	}
	defer func() { _ = f.Close() }()
	var st calendarStatus
	if json.NewDecoder(io.LimitReader(f, 4096)).Decode(&st) != nil {
		return calendarStatus{}, false
	}
	return st, true
}
