package schedule

import (
	"testing"
	"time"
)

// at returns a time in the week of Monday 2026-10-26 to Sunday 2026-11-01:
// days 26 to 31 are October, day 1 is November.
func at(day int, hh, mm int) time.Time {
	month := time.October
	if day < 26 {
		month = time.November
	}
	return time.Date(2026, month, day, hh, mm, 0, 0, time.UTC)
}

func TestContains(t *testing.T) {
	cases := []struct {
		spec string
		in   []time.Time
		out  []time.Time
	}{
		{"Mon-Fri 19:00-08:00",
			[]time.Time{at(26, 19, 0), at(26, 23, 59), at(27, 7, 59), at(31, 7, 0) /* Sat morning, after Fri */},
			[]time.Time{at(26, 18, 59), at(27, 8, 0), at(26, 7, 0) /* Mon morning, after Sun */, at(31, 20, 0), at(1, 7, 0)}},
		{"Sat,Sun",
			[]time.Time{at(31, 0, 0), at(1, 23, 59)},
			[]time.Time{at(30, 23, 59), at(26, 0, 0)}},
		{"12:00-13:00",
			[]time.Time{at(26, 12, 0), at(1, 12, 59)},
			[]time.Time{at(26, 13, 0), at(26, 11, 59)}},
		{"fri-mon 22:00-24:00",
			[]time.Time{at(30, 22, 0), at(1, 23, 59), at(26, 22, 30)},
			[]time.Time{at(27, 22, 0), at(31, 0, 30) /* 24:00 doesn't wrap */}},
		{"weekdays", []time.Time{at(26, 3, 0), at(30, 23, 0)}, []time.Time{at(31, 3, 0)}},
		{"daily 00:00-24:00", []time.Time{at(26, 0, 0), at(1, 23, 59)}, nil},
	}
	for _, c := range cases {
		w, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		for _, tm := range c.in {
			if !w.Contains(tm) {
				t.Errorf("%q should contain %s", c.spec, tm.Format("Mon 15:04"))
			}
		}
		for _, tm := range c.out {
			if w.Contains(tm) {
				t.Errorf("%q should not contain %s", c.spec, tm.Format("Mon 15:04"))
			}
		}
	}
}

func TestLocalTime(t *testing.T) {
	w, _ := Parse("19:00-08:00")
	zone := time.FixedZone("", 5*3600+1800)
	// 14:00 UTC is 19:30 at +05:30.
	if !w.Contains(at(26, 14, 0).In(zone)) || w.Contains(at(26, 14, 0)) {
		t.Error("windows are read in the time's own location")
	}
}

func TestParseErrors(t *testing.T) {
	for _, spec := range []string{
		"", "Mon Tue 10:00-11:00", "Mon-Fry", "Mon,,Tue", "10:00", "10:00-", "25:00-26:00", "10:60-11:00",
		"10:00-10:00", "24:00-08:00", "1000-1100", "Mon 10:00-11:00 extra", "-10:00-11:00", "+1:00-02:00",
	} {
		if w, err := Parse(spec); err == nil {
			t.Errorf("%q parsed as %v", spec, w)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	for spec, want := range map[string]string{
		"mon-fri 19:00-08:00": "Mon-Fri 19:00-08:00",
		"sat,sun":             "Sat,Sun",
		"weekends 9:00-12:30": "Sat,Sun 09:00-12:30",
		"12:00-13:00":         "12:00-13:00",
		"daily":               "daily",
		"fri-mon":             "Mon,Fri,Sat,Sun",
		"Tue,Thu 22:00-24:00": "Tue,Thu 22:00-24:00",
	} {
		w, err := Parse(spec)
		if err != nil {
			t.Fatalf("%q: %v", spec, err)
		}
		if got := w.String(); got != want {
			t.Errorf("%q renders %q, want %q", spec, got, want)
		}
		if back, err := Parse(w.String()); err != nil || back != w {
			t.Errorf("%q does not round-trip: %v %v", spec, back, err)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"Mon-Fri 19:00-08:00", "Sat,Sun", "12:00-13:00", "weekdays 9:00-24:00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		w, err := Parse(spec)
		if err != nil {
			return
		}
		back, err := Parse(w.String())
		if err != nil || back != w {
			t.Fatalf("%q → %q does not round-trip: %v", spec, w.String(), err)
		}
		_ = w.Contains(at(26, 12, 0))
	})
}
