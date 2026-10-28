package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/features"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/nudge"
)

func TestMissingFileIsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), FileName))
	if err != nil || !reflect.DeepEqual(c, Default()) {
		t.Fatalf("%+v, %v", c, err)
	}
	if err := Default().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestRenderRoundTrips(t *testing.T) {
	c := Default()
	c.Privacy.PrivateHours = []string{"Mon-Fri 19:00-08:00", "Sat,Sun"}
	c.Privacy.ExcludeApps = []string{"com.spotify.client", `odd "name" \ here`}
	c.Privacy.AggregateOnly = true
	c.Privacy.ExcludeDomains = []string{"bank.example.com"}
	c.Privacy.TitleClassification = true
	// Dotted keys must be quoted, or TOML reads them as nested tables.
	c.Classify.Overrides = map[string]string{"notion.id": "comms", "domain:github.com": "docs", `odd "app" \ id`: "other"}
	c.Retention.MinutesDays = 30
	c.Nudges.Enabled, c.Nudges.WakingHours, c.Nudges.OvernightGap = false, "10:00-02:00", false
	back, err := Parse(Render(c))
	if err != nil {
		t.Fatalf("%v\n%s", err, Render(c))
	}
	if !reflect.DeepEqual(back, c) {
		t.Errorf("round trip:\n got %+v\nwant %+v", back, c)
	}
	for _, def := range []Config{Default()} {
		if back, err := Parse(Render(def)); err != nil || !reflect.DeepEqual(back, def) {
			t.Errorf("defaults round trip: %+v, %v", back, err)
		}
	}
}

func TestNudgeSettings(t *testing.T) {
	s := Default().NudgeSettings()
	if !s.Enabled || len(s.Off) != 0 || s.Waking.String() != "07:00-23:00" {
		t.Errorf("defaults: %+v", s)
	}
	c := Default()
	c.Nudges.DayOff = false
	if s := c.NudgeSettings(); !s.Off[nudge.DayOff] || len(s.Off) != 1 {
		t.Errorf("day_off off: %+v", s.Off)
	}
	for _, bad := range []string{"", "07:00", "Mon 07:00-23:00", "22:00-01:00", "nonsense"} {
		c := Default()
		c.Nudges.WakingHours = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "nudges.waking_hours") {
			t.Errorf("waking hours %q: %v", bad, err)
		}
	}
}

func TestPartialFileKeepsDefaults(t *testing.T) {
	c, err := Parse([]byte("[privacy]\naggregate_only = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Privacy.AggregateOnly = true
	if !reflect.DeepEqual(c, want) {
		t.Errorf("%+v", c)
	}
}

func TestParseRejects(t *testing.T) {
	for name, file := range map[string]string{
		"unknown key":      "[privacy]\naggregateonly = true\n",
		"unknown table":    "[collect]\ntitles = true\n",
		"wrong type":       "[privacy]\naggregate_only = \"yes\"\n",
		"bad window":       "[privacy]\nprivate_hours = [\"Mon-Fry\"]\n",
		"empty app":        "[privacy]\nexclude_apps = [\" \"]\n",
		"control char":     "[privacy]\nexclude_apps = [\"a\\u0007b\"]\n",
		"retention raised": "[retention]\nminutes_days = 365\n",
		"retention zero":   "[retention]\nraw_events_days = 0\n",
		"daily too short":  "[retention]\ndaily_days = 10\n",
		"not toml":         "privacy = {",
	} {
		if c, err := Parse([]byte(file)); err == nil {
			t.Errorf("%s: accepted as %+v", name, c)
		}
	}
	_, err := Parse([]byte("[retention]\nminutes_days = 365\ndaily_days = 10\n"))
	if err == nil || !strings.Contains(err.Error(), "minutes_days") || !strings.Contains(err.Error(), "daily_days") {
		t.Errorf("every problem is reported: %v", err)
	}
}

func TestNormalize(t *testing.T) {
	c, err := Parse([]byte(`[privacy]
exclude_apps = ["com.Spotify.client", " com.spotify.client ", "Slack.exe"]
private_hours = ["Sat,Sun", "Sat,Sun"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Privacy.ExcludeApps, []string{"com.Spotify.client", "Slack.exe"}) ||
		!reflect.DeepEqual(c.Privacy.PrivateHours, []string{"Sat,Sun"}) {
		t.Errorf("%+v", c.Privacy)
	}
}

func TestGetSetAddRemoveUnset(t *testing.T) {
	c := Default()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.Set("privacy.aggregate_only", []string{"on"}))
	must(c.Set("retention.minutes_days", []string{"30"}))
	must(c.Set("privacy.private_hours", []string{"Mon-Fri 19:00-08:00"}))
	must(c.Set("nudges.weekend_work", []string{"off"}))
	must(c.Set("nudges.waking_hours", []string{"8:00-1:30"}))
	must(c.Add("privacy.exclude_apps", []string{"com.a", "com.b", "COM.A"}))
	if n, err := c.Remove("privacy.exclude_apps", []string{"com.B", "com.z"}); err != nil || n != 1 {
		t.Fatalf("remove: %d, %v", n, err)
	}
	for k, want := range map[string][]string{
		"privacy.aggregate_only": {"true"},
		"retention.minutes_days": {"30"},
		"privacy.private_hours":  {"Mon-Fri 19:00-08:00"},
		"privacy.exclude_apps":   {"com.a"},
		"nudges.weekend_work":    {"false"},
		"nudges.waking_hours":    {"08:00-01:30"},
	} {
		if got, err := c.Get(k); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %q, %v", k, got, err)
		}
	}
	must(c.Unset("retention.minutes_days"))
	must(c.Unset("privacy.exclude_apps"))
	if c.Retention.MinutesDays != 180 || len(c.Privacy.ExcludeApps) != 0 {
		t.Errorf("unset: %+v", c)
	}

	for _, bad := range []struct {
		key    string
		values []string
	}{
		{"privacy.nope", []string{"x"}},
		{"privacy.aggregate_only", []string{"maybe"}},
		{"privacy.aggregate_only", nil},
		{"retention.minutes_days", []string{"-3"}},
		{"retention.minutes_days", []string{"30", "40"}},
		{"retention.minutes_days", []string{"3d"}},
	} {
		if err := c.Set(bad.key, bad.values); err == nil {
			t.Errorf("set %s %q accepted", bad.key, bad.values)
		}
	}
	if err := c.Add("retention.minutes_days", []string{"1"}); err == nil {
		t.Error("add to a number accepted")
	}
	if len(Keys()) != 22 || !IsList("privacy.exclude_apps") || IsList("privacy.aggregate_only") {
		t.Errorf("keys: %v", Keys())
	}
}

func TestSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", FileName)
	c := Default()
	c.Retention.MinutesDays = 400
	if err := Save(path, c); err == nil {
		t.Fatal("invalid config saved")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file written for an invalid config: %v", err)
	}
	c.Retention.MinutesDays = 90
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || got.Retention.MinutesDays != 90 {
		t.Fatalf("%+v, %v", got, err)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left: %v", entries)
	}
}

func TestOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("# "+strings.Repeat("x", maxFileSize)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("oversized file loaded")
	}
}

func TestWatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w := NewWatcher(path)
	if _, changed, _ := w.Check(); changed {
		t.Fatal("no file, no change")
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("[privacy]\naggregate_only = true\n")
	if c, changed, err := w.Check(); !changed || err != nil || !c.Privacy.AggregateOnly {
		t.Fatalf("created: %+v %v %v", c, changed, err)
	}
	if _, changed, _ := w.Check(); changed {
		t.Fatal("unchanged file reported as changed")
	}
	write("[privacy]\naggregate_only = maybe\n")
	if _, changed, err := w.Check(); !changed || err == nil {
		t.Fatalf("invalid edit: %v %v", changed, err)
	}
	if _, changed, _ := w.Check(); changed {
		t.Fatal("the same invalid file is reported once")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if c, changed, err := w.Check(); !changed || err != nil || !reflect.DeepEqual(c, Default()) {
		t.Fatalf("removed: %+v %v %v", c, changed, err)
	}
}

func TestDerived(t *testing.T) {
	c := Default()
	c.Privacy.PrivateHours = []string{"Sat,Sun"}
	if s := c.PrivateHours(); len(s) != 1 || !s.Contains(time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("private hours %v", s)
	}
	raw, minutes, _, daily, _ := c.RetentionPeriods()
	if raw != 7*24*time.Hour || minutes != 180*24*time.Hour || daily != 730*24*time.Hour {
		t.Errorf("periods %v %v %v", raw, minutes, daily)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(Render(Default()))
	f.Add([]byte("[privacy]\nprivate_hours = [\"Mon-Fri 19:00-08:00\"]\nexclude_apps = [\"a\"]\n"))
	f.Add([]byte("[privacy]\nexclude_domains = [\"a.example\"]\n[classify.overrides]\n\"a.b\" = \"code\"\n\"domain:x.example\" = \"docs\"\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := Parse(b)
		if err != nil {
			return
		}
		back, err := Parse(Render(c))
		if err != nil || !reflect.DeepEqual(back, c) {
			t.Fatalf("valid config does not round-trip: %v\n%s", err, Render(c))
		}
	})
}

func TestProfile(t *testing.T) {
	c, err := Parse([]byte("[profile]\nrole = \"PM\"\ncore_hours = \"7:30-16:00\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Role() != features.RolePM || c.Profile.CoreHours != "07:30-16:00" {
		t.Errorf("profile %+v", c.Profile)
	}
	p := c.FeatureParams()
	if p.CoreStart != 7*time.Hour+30*time.Minute || p.CoreEnd != 16*time.Hour || p.Key() == features.DefaultKey {
		t.Errorf("params %+v", p)
	}
	if Default().FeatureParams().Key() != features.DefaultKey {
		t.Error("the default profile must keep the default feature key, so stored days aren't recomputed")
	}
	for _, bad := range []string{
		"role = \"manager\"", "core_hours = \"18:00-09:00\"", "core_hours = \"Mon-Fri 09:00-17:00\"",
		"core_hours = \"09:00-09:30\"", "core_hours = \"weekdays\"", "core_hours = \"9-5\"", "core_hours = \"\"",
	} {
		if _, err := Parse([]byte("[profile]\n" + bad + "\n")); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if err := c.Set("profile.core_hours", []string{"10:00-19:00"}); err != nil || c.Profile.CoreHours != "10:00-19:00" {
		t.Errorf("set: %v %+v", err, c.Profile)
	}
	if err := c.Unset("profile.role"); err != nil || c.Role() != features.RoleDeveloper {
		t.Errorf("unset: %v %+v", err, c.Profile)
	}
}

func TestUpdateCheck(t *testing.T) {
	if Default().Update.Check != UpdateOff {
		t.Error("the update check must be off by default")
	}
	c, err := Parse([]byte("[update]\ncheck = \" Weekly \"\n"))
	if err != nil || c.Update.Check != UpdateWeekly {
		t.Errorf("weekly: %v, %v", c.Update.Check, err)
	}
	if _, err := Parse([]byte("[update]\ncheck = \"daily\"\n")); err == nil || !strings.Contains(err.Error(), "update.check") {
		t.Errorf("daily: %v", err)
	}
	if err := c.Unset("update.check"); err != nil || c.Update.Check != UpdateOff {
		t.Errorf("unset: %v, %v", c.Update.Check, err)
	}
}

func TestTitlesAndOverrides(t *testing.T) {
	c, err := Parse([]byte(`[privacy]
exclude_domains = ["WWW.Bank.example.", "domain:bank.example", "health.example"]
title_classification = true

[classify.overrides]
"Notion.ID" = " Comms "
"domain:WWW.GitHub.com" = "code"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Privacy.ExcludeDomains, []string{"bank.example", "health.example"}) || !c.Privacy.TitleClassification {
		t.Errorf("privacy %+v", c.Privacy)
	}
	if want := map[string]string{"notion.id": "comms", "domain:github.com": "code"}; !reflect.DeepEqual(c.Classify.Overrides, want) {
		t.Errorf("overrides %v", c.Classify.Overrides)
	}
	if got := c.Exclusions(); !reflect.DeepEqual(got, []string{"domain:bank.example", "domain:health.example"}) {
		t.Errorf("exclusions %q", got)
	}
	if got := c.Overrides(); got["domain:github.com"] != model.CategoryCode {
		t.Errorf("Overrides() %v", got)
	}

	for name, file := range map[string]string{
		"bad category":     "[classify.overrides]\n\"notion.id\" = \"work\"\n",
		"private override": "[classify.overrides]\n\"notion.id\" = \"private\"\n",
		"bad domain key":   "[classify.overrides]\n\"domain:not a site\" = \"code\"\n",
		"empty app key":    "[classify.overrides]\n\" \" = \"code\"\n",
		"bad site":         "[privacy]\nexclude_domains = [\"https://x.example/path\"]\n",
		"unquoted dotted":  "[classify.overrides]\nnotion.id = \"comms\"\n",
		"title not bool":   "[privacy]\ntitle_classification = \"on\"\n",
	} {
		if c, err := Parse([]byte(file)); err == nil {
			t.Errorf("%s: accepted as %+v", name, c)
		}
	}

	d := Default()
	if err := d.Set("classify.overrides", []string{"notion.id=docs", "domain:GitHub.com=code"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Add("classify.overrides", []string{"Notion.id=comms", "zoom=meeting"}); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Remove("classify.overrides", []string{"zoom", "domain:github.com=code", "absent"}); err != nil || n != 2 {
		t.Fatalf("remove: %d, %v", n, err)
	}
	if got, _ := d.Get("classify.overrides"); !reflect.DeepEqual(got, []string{"notion.id=comms"}) {
		t.Errorf("get %q", got)
	}
	if err := d.Set("classify.overrides", []string{"no-equals"}); err == nil {
		t.Error("an item without = accepted")
	}
	if err := d.Unset("classify.overrides"); err != nil || len(d.Classify.Overrides) != 0 || d.Classify.Overrides == nil {
		t.Errorf("unset: %v %v", d.Classify.Overrides, err)
	}
	if !IsList("classify.overrides") || IsList("privacy.title_classification") {
		t.Error("IsList")
	}
}
