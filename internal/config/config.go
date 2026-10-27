// Package config is the TOML settings file (system design §3.4): its
// schema, defaults and validation. There is one file,
// <config dir>/burnout/config.toml. It holds no secrets: those live in the
// OS keystore. A missing file means every default; an invalid one is
// never half-applied.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/schedule"
)

// FileName is the config file's name inside the config directory.
const FileName = "config.toml"

// maxFileSize bounds what Load reads; a real config is a few hundred bytes.
const maxFileSize = 64 << 10

// Config is the whole file.
type Config struct {
	Profile   Profile   `toml:"profile"`
	Privacy   Privacy   `toml:"privacy"`
	Classify  Classify  `toml:"classify"`
	Retention Retention `toml:"retention"`
	Calendar  Calendar  `toml:"calendar"`
	Update    Update    `toml:"update"`
}

// Update is the opt-in update check (privacy & security §6). It only ever
// tells the user about a new release; installing is `burnout update`.
type Update struct {
	// Check is "off" (the default) or "weekly".
	Check string `toml:"check"`
}

// The update.check values.
const (
	UpdateOff    = "off"
	UpdateWeekly = "weekly"
)

// Calendar holds the calendar feed settings. The feed URL itself is a
// secret and lives in the OS keystore, never here (system design §3.4).
type Calendar struct {
	ICS struct {
		// Enabled turns fetching the feed on or off without forgetting it
		// (privacy & security §6).
		Enabled bool `toml:"enabled"`
	} `toml:"ics"`
}

// Profile is who the user is, for scoring (system design §4.6, §8.1).
type Profile struct {
	// Role picks the feature weights: developer, student, researcher or pm.
	Role string `toml:"role"`
	// CoreHours are the usual working hours on weekdays, "HH:MM-HH:MM" in
	// local time. Work outside them counts as after-hours.
	CoreHours string `toml:"core_hours"`
}

// Privacy holds the user's privacy controls (privacy & security §5.1).
type Privacy struct {
	// PrivateHours are recurring windows when nothing is recorded, in the
	// form schedule.Parse reads ("Mon-Fri 19:00-08:00").
	PrivateHours []string `toml:"private_hours"`
	// ExcludeApps are app ids (macOS bundle ids, Windows exe names) whose
	// time is recorded as private, without their names.
	ExcludeApps []string `toml:"exclude_apps"`
	// ExcludeDomains are sites ("example.com", which covers its
	// subdomains) whose time is recorded as private. They apply to browser
	// windows when title classification is on.
	ExcludeDomains []string `toml:"exclude_domains"`
	// TitleClassification reads browser window titles to tell sites apart
	// (privacy & security §4.1). Titles stay in memory: they are redacted,
	// classified and discarded, and only the category is kept. On macOS it
	// needs the Screen Recording permission. The daemon reads it at start.
	TitleClassification bool `toml:"title_classification"`
	// AggregateOnly keeps only per-category minutes: no app names and no
	// per-window rows.
	AggregateOnly bool `toml:"aggregate_only"`
}

// Classify holds the user's own labels (system design §4.6).
type Classify struct {
	// Overrides map an app id, or a site as "domain:example.com", to a
	// category. They win over the bundled rules but never over exclusions
	// or the sensitive lists.
	Overrides map[string]string `toml:"overrides"`
}

// Retention is how many days each kind of data is kept. Each can only be
// lowered from its default (privacy & security §3).
type Retention struct {
	RawEventsDays int `toml:"raw_events_days"`
	MinutesDays   int `toml:"minutes_days"`   // attention minutes, monitor layouts, health
	IntervalsDays int `toml:"intervals_days"` // commands, agent and presence timelines
	DailyDays     int `toml:"daily_days"`     // daily features and scores
	CheckinsDays  int `toml:"checkins_days"`
}

// Default returns the documented defaults.
func Default() Config {
	c := Config{
		Profile:  Profile{Role: string(features.RoleDeveloper), CoreHours: "09:00-18:00"},
		Privacy:  Privacy{PrivateHours: []string{}, ExcludeApps: []string{}, ExcludeDomains: []string{}},
		Classify: Classify{Overrides: map[string]string{}},
		Retention: Retention{
			RawEventsDays: 7,
			MinutesDays:   180,
			IntervalsDays: 180,
			DailyDays:     730,
			CheckinsDays:  730,
		},
	}
	c.Calendar.ICS.Enabled = true // fetched only once a feed is added
	c.Update.Check = UpdateOff
	return c
}

// bounds are the allowed retention values. The upper bound is the default;
// the lower bound is what the app needs to keep working.
var bounds = []struct {
	key      string
	get      func(*Retention) *int
	min, max int
	what     string // for the file's comment
	why      string // why the minimum
}{
	{"retention.raw_events_days", func(r *Retention) *int { return &r.RawEventsDays }, 1, 7,
		"hook events, for reprocessing", ""},
	{"retention.minutes_days", func(r *Retention) *int { return &r.MinutesDays }, 3, 180,
		"minute-by-minute app activity", "daily features are computed from the last few days of minutes"},
	{"retention.intervals_days", func(r *Retention) *int { return &r.IntervalsDays }, 3, 180,
		"commands, agent sessions, presence", "daily features are computed from the last few days of intervals"},
	{"retention.daily_days", func(r *Retention) *int { return &r.DailyDays }, 42, 730,
		"daily features and strain scores", "the strain score needs 28 days of baseline after 14 days of calibration"},
	{"retention.checkins_days", func(r *Retention) *int { return &r.CheckinsDays }, 14, 730,
		"check-in answers", "the weekly review compares the last two check-ins"},
}

// maxApps and maxAppLen bound exclude_apps; maxWindows bounds private_hours.
const (
	maxApps    = 500
	maxAppLen  = 256
	maxWindows = 50
)

// Validate reports every problem in c, one per line, naming the key.
func (c Config) Validate() error {
	var errs []error
	if !features.Role(c.Profile.Role).Valid() {
		names := make([]string, 0, len(features.Roles()))
		for _, r := range features.Roles() {
			names = append(names, string(r))
		}
		errs = append(errs, fmt.Errorf("profile.role: %q isn't a role (%s)", c.Profile.Role, strings.Join(names, ", ")))
	}
	if _, err := parseCoreHours(c.Profile.CoreHours); err != nil {
		errs = append(errs, fmt.Errorf("profile.core_hours: %w", err))
	}
	if len(c.Privacy.PrivateHours) > maxWindows {
		errs = append(errs, fmt.Errorf("privacy.private_hours: at most %d windows", maxWindows))
	}
	for _, spec := range c.Privacy.PrivateHours {
		if _, err := schedule.Parse(spec); err != nil {
			errs = append(errs, fmt.Errorf("privacy.private_hours: %s", strings.TrimPrefix(err.Error(), "schedule: ")))
		}
	}
	if len(c.Privacy.ExcludeApps) > maxApps {
		errs = append(errs, fmt.Errorf("privacy.exclude_apps: at most %d apps", maxApps))
	}
	for _, app := range c.Privacy.ExcludeApps {
		if err := checkAppID(app); err != nil {
			errs = append(errs, fmt.Errorf("privacy.exclude_apps: %w", err))
		}
	}
	if len(c.Privacy.ExcludeDomains) > maxApps {
		errs = append(errs, fmt.Errorf("privacy.exclude_domains: at most %d sites", maxApps))
	}
	for _, d := range c.Privacy.ExcludeDomains {
		if !classify.IsHostname(d) {
			errs = append(errs, fmt.Errorf("privacy.exclude_domains: %.40q is not a site like example.com", d))
		}
	}
	if len(c.Classify.Overrides) > maxApps {
		errs = append(errs, fmt.Errorf("classify.overrides: at most %d overrides", maxApps))
	}
	for _, k := range slices.Sorted(maps.Keys(c.Classify.Overrides)) {
		if err := checkOverride(k, c.Classify.Overrides[k]); err != nil {
			errs = append(errs, fmt.Errorf("classify.overrides: %w", err))
		}
	}
	if c.Update.Check != UpdateOff && c.Update.Check != UpdateWeekly {
		errs = append(errs, fmt.Errorf("update.check: %q: want %q or %q", c.Update.Check, UpdateOff, UpdateWeekly))
	}
	r := c.Retention
	for _, b := range bounds {
		v := *b.get(&r)
		switch {
		case v > b.max:
			errs = append(errs, fmt.Errorf("%s: %d is above the default of %d; retention can only be lowered", b.key, v, b.max))
		case v < b.min:
			msg := fmt.Sprintf("%s: %d is below the minimum of %d", b.key, v, b.min)
			if b.why != "" {
				msg += " (" + b.why + ")"
			}
			errs = append(errs, errors.New(msg))
		}
	}
	return errors.Join(errs...)
}

func checkAppID(app string) error {
	if strings.TrimSpace(app) == "" {
		return errors.New("an app id is empty")
	}
	if len(app) > maxAppLen || !utf8.ValidString(app) || strings.IndexFunc(app, unicode.IsControl) >= 0 {
		return fmt.Errorf("%.40q is not an app id", app)
	}
	return nil
}

// checkOverride checks one classify.overrides entry.
func checkOverride(key, cat string) error {
	if d, ok := strings.CutPrefix(key, classify.DomainPrefix); ok {
		if !classify.IsHostname(d) {
			return fmt.Errorf("%.40q: want a site like domain:example.com", key)
		}
	} else if err := checkAppID(key); err != nil {
		return err
	}
	switch c := model.Category(cat); {
	case c == model.CategoryPrivate:
		return fmt.Errorf("%.40q: to make it private, add it to privacy.exclude_apps or privacy.exclude_domains", key)
	case !c.Valid():
		return fmt.Errorf("%.40q: %.40q isn't a category (%s)", key, cat, strings.Join(overrideCategories(), ", "))
	}
	return nil
}

// overrideCategories are the categories an override can name.
func overrideCategories() []string {
	var names []string
	for _, k := range model.Categories() {
		if k != model.CategoryPrivate {
			names = append(names, string(k))
		}
	}
	return names
}

// Overrides returns classify.overrides as categories. Call it on a valid
// config.
func (c Config) Overrides() map[string]model.Category {
	out := make(map[string]model.Category, len(c.Classify.Overrides))
	for k, v := range c.Classify.Overrides {
		out[k] = model.Category(v)
	}
	return out
}

// Exclusions returns the excluded apps and sites, the sites as
// "domain:example.com".
func (c Config) Exclusions() []string {
	out := slices.Clone(c.Privacy.ExcludeApps)
	for _, d := range c.Privacy.ExcludeDomains {
		out = append(out, classify.DomainPrefix+d)
	}
	return out
}

// minCore is the shortest core-hours span accepted.
const minCore = time.Hour

// parseCoreHours reads "HH:MM-HH:MM": one span within a day, every day.
func parseCoreHours(spec string) (schedule.Window, error) {
	w, err := schedule.Parse(spec)
	switch {
	case err != nil || !strings.Contains(spec, ":"):
		return w, fmt.Errorf("%q: want a time range like 09:00-18:00", spec)
	case w.Days != [7]bool{true, true, true, true, true, true, true} || strings.ContainsAny(strings.TrimSpace(spec), " "):
		return w, fmt.Errorf("%q: give times only; core hours apply on weekdays", spec)
	case w.End <= w.Start:
		return w, fmt.Errorf("%q: core hours can't run past midnight", spec)
	case w.End-w.Start < minCore:
		return w, fmt.Errorf("%q: core hours must span at least an hour", spec)
	}
	return w, nil
}

// Role returns the profile's role. Call it on a valid config.
func (c Config) Role() features.Role { return features.Role(c.Profile.Role) }

// FeatureParams returns the feature definitions for this profile: the
// defaults with the user's core hours. Call it on a valid config.
func (c Config) FeatureParams() features.Params {
	p := features.DefaultParams()
	if w, err := parseCoreHours(c.Profile.CoreHours); err == nil {
		p.CoreStart, p.CoreEnd = w.Start, w.End
	}
	return p
}

// PrivateHours returns the parsed private hours. Call it on a valid config.
func (c Config) PrivateHours() schedule.Schedule {
	var s schedule.Schedule
	for _, spec := range c.Privacy.PrivateHours {
		if w, err := schedule.Parse(spec); err == nil {
			s = append(s, w)
		}
	}
	return s
}

// RetentionPeriods returns the retention settings as durations.
func (c Config) RetentionPeriods() (rawEvents, minutes, intervals, daily, checkins time.Duration) {
	d := func(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }
	r := c.Retention
	return d(r.RawEventsDays), d(r.MinutesDays), d(r.IntervalsDays), d(r.DailyDays), d(r.CheckinsDays)
}

// Path returns the config file's path inside dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Load reads and validates the file at path. A missing file yields the
// defaults. Keys missing from the file keep their defaults; unknown keys
// are an error, so a typo never silently leaves a privacy control off.
func Load(path string) (Config, error) {
	b, err := readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	return Parse(b)
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the user's own config file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var buf bytes.Buffer
	n, err := buf.ReadFrom(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if n > maxFileSize {
		return nil, fmt.Errorf("config: %s is larger than %d KB", path, maxFileSize>>10)
	}
	return buf.Bytes(), nil
}

// Parse decodes and validates a config file's contents.
func Parse(b []byte) (Config, error) {
	c := Default()
	md, err := toml.Decode(string(b), &c)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("config: unknown keys: %s", strings.Join(keys, ", "))
	}
	c.normalize()
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

// normalize writes private hours in one form ("mon-fri 19:00-8:00"
// becomes "Mon-Fri 19:00-08:00"), drops duplicate entries (app ids compare
// without case) and keeps empty lists non-nil, so they render as [].
func (c *Config) normalize() {
	hours := slices.Clone(c.Privacy.PrivateHours) // never write to the caller's array
	for i, spec := range hours {
		if w, err := schedule.Parse(spec); err == nil {
			hours[i] = w.String()
		}
	}
	c.Privacy.PrivateHours = dedupe(hours, strings.TrimSpace)
	c.Profile.Role = strings.ToLower(strings.TrimSpace(c.Profile.Role))
	c.Update.Check = strings.ToLower(strings.TrimSpace(c.Update.Check))
	if w, err := parseCoreHours(c.Profile.CoreHours); err == nil {
		c.Profile.CoreHours = w.String()
	}
	c.Privacy.ExcludeApps = dedupe(c.Privacy.ExcludeApps, func(s string) string { return strings.ToLower(strings.TrimSpace(s)) })
	domains := make([]string, len(c.Privacy.ExcludeDomains))
	for i, d := range c.Privacy.ExcludeDomains {
		domains[i] = normalizeDomain(d)
	}
	c.Privacy.ExcludeDomains = dedupe(domains, strings.TrimSpace)
	c.Classify.Overrides = normalizedOverrides(c.Classify.Overrides)
}

// normalizedOverrides lower-cases keys and categories and tidies domains.
// Keys that become equal keep the value of the last in sorted order.
func normalizedOverrides(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		key := strings.ToLower(strings.TrimSpace(k))
		if d, ok := strings.CutPrefix(key, classify.DomainPrefix); ok {
			key = classify.DomainPrefix + normalizeDomain(d)
		}
		out[key] = strings.ToLower(strings.TrimSpace(m[k]))
	}
	return out
}

// normalizeDomain accepts "Example.com", "www.example.com." and
// "domain:example.com" for example.com.
func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	return classify.NormalizeDomain(strings.TrimPrefix(d, classify.DomainPrefix))
}

func dedupe(list []string, key func(string) string) []string {
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, v := range list {
		v = strings.TrimSpace(v)
		if k := key(v); !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out
}

// Save validates c and writes it to path atomically, owner-only. It
// replaces the whole file: comments other than the generated ones are lost.
func Save(path string, c Config) error {
	c.normalize()
	if err := c.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(Render(c)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = tmp.Close()
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// Render writes c as a commented TOML file.
func Render(c Config) []byte {
	var b bytes.Buffer
	b.WriteString(`# burnout settings. Change them with ` + "`burnout config set KEY VALUE`" + `, or edit this
# file: the daemon picks up changes within a few seconds. Check an edited
# file with ` + "`burnout config validate`" + `. Secrets never go in this file.

[profile]
# What your work looks like, so the strain score weighs the right things:
# developer, student, researcher or pm (project or product manager).
`)
	fmt.Fprintf(&b, "role = %s\n", quote(c.Profile.Role))
	b.WriteString(`
# Your usual weekday working hours, in local time. Work outside them counts
# as after-hours. Changing them recomputes the days whose detail is kept.
`)
	fmt.Fprintf(&b, "core_hours = %s\n", quote(c.Profile.CoreHours))
	b.WriteString(`
[privacy]
# Recurring times when nothing is recorded, in your local time. Examples:
#   "Mon-Fri 19:00-08:00"  weekday evenings into the next morning
#   "Sat,Sun"              whole weekends
#   "12:00-13:00"          every lunchtime
`)
	fmt.Fprintf(&b, "private_hours = %s\n", list(c.Privacy.PrivateHours))
	b.WriteString(`
# Apps recorded as private: their time counts, their names aren't kept.
# Use the names ` + "`burnout status`" + ` and ` + "`burnout timeline`" + ` show (macOS bundle
# ids, Windows exe names); case doesn't matter.
# Password managers and other sensitive apps are always private.
`)
	fmt.Fprintf(&b, "exclude_apps = %s\n", list(c.Privacy.ExcludeApps))
	b.WriteString(`
# Sites recorded as private, like excluded apps ("example.com" covers its
# subdomains). They apply when title_classification is on, to pages whose
# title names the site ("example" or "example.com"). Banking, payment,
# health, dating and password-manager sites are always private.
`)
	fmt.Fprintf(&b, "exclude_domains = %s\n", list(c.Privacy.ExcludeDomains))
	b.WriteString(`
# Read browser window titles to tell sites apart (GitHub is code, Gmail is
# comms). Titles stay in memory: they are redacted, classified and
# discarded; only the category is kept. On macOS it needs the Screen
# Recording permission. Takes effect when the daemon restarts.
`)
	fmt.Fprintf(&b, "title_classification = %t\n", c.Privacy.TitleClassification)
	b.WriteString(`
# Keep only time per category (code, comms, browser, …): no app names and
# no per-window detail. Days already recorded are reduced overnight.
`)
	fmt.Fprintf(&b, "aggregate_only = %t\n", c.Privacy.AggregateOnly)
	b.WriteString(`
[classify.overrides]
# Your own categories for apps, or for sites as "domain:example.com":
#   "com.spotify.client" = "other"
#   "domain:example.com" = "docs"
`)
	fmt.Fprintf(&b, "# Categories: %s.\n", strings.Join(overrideCategories(), ", "))
	for _, k := range slices.Sorted(maps.Keys(c.Classify.Overrides)) {
		fmt.Fprintf(&b, "%s = %s\n", quote(k), quote(c.Classify.Overrides[k]))
	}
	b.WriteString(`
[retention]
# How many days each kind of data is kept. Each can only be lowered.
# Shorter periods apply at the next nightly clean-up.
`)
	for _, bd := range bounds {
		setting := fmt.Sprintf("%s = %d", strings.TrimPrefix(bd.key, "retention."), *bd.get(&c.Retention))
		fmt.Fprintf(&b, "%-22s # %s (%d to %d)\n", setting, bd.what, bd.min, bd.max)
	}
	b.WriteString(`
[calendar.ics]
# Fetch the calendar feed added with ` + "`burnout calendar add`" + ` (its URL is kept in
# your OS keystore, not here). false stops fetching and keeps the feed.
`)
	fmt.Fprintf(&b, "enabled = %t\n", c.Calendar.ICS.Enabled)
	b.WriteString(`
[update]
# "weekly" asks GitHub once a week whether a newer release exists and shows
# it in the tray and ` + "`burnout doctor`" + `; it never installs anything (run
# ` + "`burnout update`" + `). "off" makes no update checks at all.
`)
	fmt.Fprintf(&b, "check = %s\n", quote(c.Update.Check))
	return b.Bytes()
}

// list renders strings as a TOML array, one item per line when there are
// several.
func list(items []string) string {
	switch len(items) {
	case 0:
		return "[]"
	case 1:
		return "[" + quote(items[0]) + "]"
	}
	var b strings.Builder
	b.WriteString("[\n")
	for _, s := range items {
		b.WriteString("  " + quote(s) + ",\n")
	}
	b.WriteString("]")
	return b.String()
}

// quote renders s as a TOML basic string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Keys lists every settable key, in file order.
func Keys() []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.name
	}
	return out
}

// key is one setting reachable by `burnout config get|set`.
type key struct {
	name string
	list func(*Config) *[]string
	flag func(*Config) *bool
	num  func(*Config) *int
	text func(*Config) *string
	dict func(*Config) *map[string]string // a list of "key=value" items to the CLI
}

var keys = func() []key {
	k := []key{
		{name: "profile.role", text: func(c *Config) *string { return &c.Profile.Role }},
		{name: "profile.core_hours", text: func(c *Config) *string { return &c.Profile.CoreHours }},
		{name: "privacy.private_hours", list: func(c *Config) *[]string { return &c.Privacy.PrivateHours }},
		{name: "privacy.exclude_apps", list: func(c *Config) *[]string { return &c.Privacy.ExcludeApps }},
		{name: "privacy.exclude_domains", list: func(c *Config) *[]string { return &c.Privacy.ExcludeDomains }},
		{name: "privacy.title_classification", flag: func(c *Config) *bool { return &c.Privacy.TitleClassification }},
		{name: "privacy.aggregate_only", flag: func(c *Config) *bool { return &c.Privacy.AggregateOnly }},
		{name: "classify.overrides", dict: func(c *Config) *map[string]string { return &c.Classify.Overrides }},
	}
	for _, b := range bounds {
		get := b.get
		k = append(k, key{name: b.key, num: func(c *Config) *int { return get(&c.Retention) }})
	}
	return append(k,
		key{name: "calendar.ics.enabled", flag: func(c *Config) *bool { return &c.Calendar.ICS.Enabled }},
		key{name: "update.check", text: func(c *Config) *string { return &c.Update.Check }},
	)
}()

func lookup(name string) (key, error) {
	for _, k := range keys {
		if k.name == name {
			return k, nil
		}
	}
	return key{}, fmt.Errorf("config: unknown key %q (keys: %s)", name, strings.Join(Keys(), ", "))
}

// IsList reports whether name is a list setting. classify.overrides is
// one too: a list of "key=category" items.
func IsList(name string) bool {
	k, err := lookup(name)
	return err == nil && (k.list != nil || k.dict != nil)
}

// pairs reads "key=value" items.
func pairs(name string, items []string) (map[string]string, error) {
	out := make(map[string]string, len(items))
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("config: %s: want KEY=CATEGORY, like domain:example.com=docs, not %.40q", name, it)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// Get returns a setting's value: one line per item for lists.
func (c Config) Get(name string) ([]string, error) {
	k, err := lookup(name)
	if err != nil {
		return nil, err
	}
	switch {
	case k.list != nil:
		return slices.Clone(*k.list(&c)), nil
	case k.dict != nil:
		m := *k.dict(&c)
		out := make([]string, 0, len(m))
		for _, key := range slices.Sorted(maps.Keys(m)) {
			out = append(out, key+"="+m[key])
		}
		return out, nil
	case k.flag != nil:
		return []string{fmt.Sprint(*k.flag(&c))}, nil
	case k.text != nil:
		return []string{*k.text(&c)}, nil
	default:
		return []string{fmt.Sprint(*k.num(&c))}, nil
	}
}

// Set replaces a setting. Lists take any number of values (none empties
// the list); other settings take exactly one. The result isn't validated:
// Save does that.
func (c *Config) Set(name string, values []string) error {
	k, err := lookup(name)
	if err != nil {
		return err
	}
	if k.list != nil {
		*k.list(c) = slices.Clone(values)
		return nil
	}
	if k.dict != nil {
		m, err := pairs(name, values)
		if err != nil {
			return err
		}
		*k.dict(c) = m
		c.normalize()
		return nil
	}
	if len(values) != 1 {
		return fmt.Errorf("config: %s takes one value", name)
	}
	v := values[0]
	if k.text != nil {
		*k.text(c) = v
		c.normalize()
		return nil
	}
	if k.flag != nil {
		switch strings.ToLower(v) {
		case "true", "on", "yes":
			*k.flag(c) = true
		case "false", "off", "no":
			*k.flag(c) = false
		default:
			return fmt.Errorf("config: %s: want true or false, not %q", name, v)
		}
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fmt.Errorf("config: %s: want a whole number of days, not %q", name, v)
	}
	*k.num(c) = n
	return nil
}

// Add appends values to a list setting, skipping ones already there.
func (c *Config) Add(name string, values []string) error {
	k, err := lookup(name)
	if err != nil {
		return err
	}
	if k.dict != nil {
		m, err := pairs(name, values)
		if err != nil {
			return err
		}
		maps.Copy(*k.dict(c), normalizedOverrides(m)) // a key already there gets the new category
		return nil
	}
	if k.list == nil {
		return fmt.Errorf("config: %s is not a list; use set", name)
	}
	l := k.list(c)
	*l = append(*l, values...)
	c.normalize()
	return nil
}

// Remove deletes values from a list setting (app ids compare without
// case). It reports how many were removed.
func (c *Config) Remove(name string, values []string) (int, error) {
	k, err := lookup(name)
	if err != nil {
		return 0, err
	}
	if k.dict != nil {
		// Values are keys, or key=category items as get prints them.
		m := *k.dict(c)
		n := 0
		for _, v := range values {
			key, _, _ := strings.Cut(v, "=")
			for norm := range normalizedOverrides(map[string]string{key: ""}) {
				if _, ok := m[norm]; ok {
					delete(m, norm)
					n++
				}
			}
		}
		return n, nil
	}
	if k.list == nil {
		return 0, fmt.Errorf("config: %s is not a list; use set or unset", name)
	}
	l := k.list(c)
	before := len(*l)
	*l = slices.DeleteFunc(*l, func(have string) bool {
		return slices.ContainsFunc(values, func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), have) })
	})
	return before - len(*l), nil
}

// Unset restores a setting's default.
func (c *Config) Unset(name string) error {
	def := Default()
	v, err := def.Get(name)
	if err != nil {
		return err
	}
	return c.Set(name, v)
}
