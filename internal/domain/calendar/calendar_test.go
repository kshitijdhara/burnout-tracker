package calendar

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var berlin = mustZone("Europe/Berlin")

func mustZone(name string) *time.Location {
	z, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return z
}

func opts(from, to string) Options {
	f, _ := time.ParseInLocation(time.DateOnly, from, berlin)
	t, _ := time.ParseInLocation(time.DateOnly, to, berlin)
	return Options{From: f, To: t, Zone: time.LoadLocation, Local: berlin}
}

func feed(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
		strings.Join(events, "") + "END:VCALENDAR\r\n"
}

func vevent(lines ...string) string {
	return "BEGIN:VEVENT\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func starts(bs []Block) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Start.In(berlin).Format("Mon 01-02 15:04") + "+" + b.End.Sub(b.Start).String()
		if b.Tentative {
			out[i] += " (tentative)"
		}
	}
	return out
}

func parse(t *testing.T, ics string, o Options) []string {
	t.Helper()
	bs, err := Parse(strings.NewReader(ics), o)
	if err != nil {
		t.Fatal(err)
	}
	return starts(bs)
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestSingleEvents(t *testing.T) {
	ics := feed(
		vevent("UID:a", "DTSTART:20261027T080000Z", "DTEND:20261027T090000Z", "SUMMARY:Standup CANARYsum"),
		vevent("UID:b", "DTSTART;TZID=Europe/Berlin:20261028T140000", "DURATION:PT45M", "STATUS:TENTATIVE"),
		vevent("UID:c", "DTSTART;VALUE=DATE:20261029", "DTEND;VALUE=DATE:20261030"), // all day
		vevent("UID:d", "DTSTART:20261029T100000Z", "DTEND:20261029T110000Z", "TRANSP:TRANSPARENT"),
		vevent("UID:e", "DTSTART:20261029T120000Z", "DTEND:20261029T130000Z", "STATUS:CANCELLED"),
		vevent("UID:f", "DTSTART:20261030T060000Z", "DTEND:20261031T060000Z"), // 24 h: out of office
		vevent("UID:g", "DTSTART:20261030T150000", "DTEND:20261030T153000", // floating: local
			"BEGIN:VALARM", "TRIGGER:-PT10M", "DTSTART:20200101T000000Z", "END:VALARM"),
		vevent("UID:h", "DTSTART:20261101T090000Z", "DTEND:20261101T100000Z"), // after the window
	)
	eq(t, parse(t, ics, opts("2026-10-26", "2026-11-01")),
		"Tue 10-27 09:00+1h0m0s", "Wed 10-28 14:00+45m0s (tentative)", "Fri 10-30 15:00+30m0s")
}

func TestDroppedFieldsAreNeverKept(t *testing.T) {
	long := "DESCRIPTION:" + strings.Repeat("CANARYdesc ", 5000) // far past maxLine, folded below
	var folded strings.Builder
	for i := 0; i < len(long); i += 70 {
		if i > 0 {
			folded.WriteString("\r\n ")
		}
		folded.WriteString(long[i:min(i+70, len(long))])
	}
	ics := feed(vevent("UID:a", "DTSTART:20261027T080000Z", "DTEND:20261027T090000Z", folded.String(),
		"ATTENDEE;CN=Someone CANARYatt:mailto:x@y", "LOCATION:Room CANARYloc"))
	bs, err := Parse(strings.NewReader(ics), opts("2026-10-26", "2026-11-01"))
	if err != nil || len(bs) != 1 {
		t.Fatalf("%v %v", bs, err)
	}
	if s := fmt.Sprintf("%+v", bs); strings.Contains(s, "CANARY") {
		t.Errorf("a dropped field reached the output: %s", s)
	}
	// A kept property that is too long drops its event rather than guessing.
	bad := feed(vevent("UID:a", "DTSTART:2026"+strings.Repeat("1", maxLine)+"T080000Z", "DTEND:20261027T090000Z"))
	if got := parse(t, bad, opts("2026-10-26", "2026-11-01")); len(got) != 0 {
		t.Errorf("event with an oversized DTSTART kept: %v", got)
	}
}

func TestWeeklyWithExceptionsAndOverride(t *testing.T) {
	ics := feed(
		vevent("UID:standup", "DTSTART;TZID=Europe/Berlin:20261005T093000", "DTEND;TZID=Europe/Berlin:20261005T094500",
			"RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR", "EXDATE;TZID=Europe/Berlin:20261028T093000"),
		// Friday's instance moved to 11:00 and made longer.
		vevent("UID:standup", "RECURRENCE-ID;TZID=Europe/Berlin:20261030T093000",
			"DTSTART;TZID=Europe/Berlin:20261030T110000", "DTEND;TZID=Europe/Berlin:20261030T113000"),
	)
	// Daylight saving ends on 25 October: the meeting stays at 09:30 local.
	eq(t, parse(t, ics, opts("2026-10-23", "2026-11-03")),
		"Fri 10-23 09:30+15m0s", "Mon 10-26 09:30+15m0s", "Fri 10-30 11:00+30m0s", "Mon 11-02 09:30+15m0s")
	cancelled := feed(
		vevent("UID:s", "DTSTART:20261026T080000Z", "DTEND:20261026T090000Z", "RRULE:FREQ=DAILY;COUNT=3"),
		vevent("UID:s", "RECURRENCE-ID:20261027T080000Z", "DTSTART:20261027T080000Z", "DTEND:20261027T090000Z", "STATUS:CANCELLED"),
	)
	eq(t, parse(t, cancelled, opts("2026-10-20", "2026-11-03")), "Mon 10-26 09:00+1h0m0s", "Wed 10-28 09:00+1h0m0s")
}

func TestRecurrenceRules(t *testing.T) {
	for _, tc := range []struct {
		name, dtstart, rrule string
		want                 []string
	}{
		{"every other week", "20261006T100000", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU",
			[]string{"Tue 10-06 10:00+1h0m0s", "Tue 10-20 10:00+1h0m0s", "Tue 11-03 10:00+1h0m0s", "Tue 11-17 10:00+1h0m0s"}},
		{"daily count", "20261026T100000", "FREQ=DAILY;COUNT=3",
			[]string{"Mon 10-26 10:00+1h0m0s", "Tue 10-27 10:00+1h0m0s", "Wed 10-28 10:00+1h0m0s"}},
		{"weekdays until a date", "20261029T100000", "FREQ=DAILY;BYDAY=MO,TU,WE,TH,FR;UNTIL=20261102",
			[]string{"Thu 10-29 10:00+1h0m0s", "Fri 10-30 10:00+1h0m0s", "Mon 11-02 10:00+1h0m0s"}},
		{"first Monday", "20261005T100000", "FREQ=MONTHLY;BYDAY=1MO",
			[]string{"Mon 10-05 10:00+1h0m0s", "Mon 11-02 10:00+1h0m0s"}},
		{"last Friday", "20261030T100000", "FREQ=MONTHLY;BYDAY=-1FR",
			[]string{"Fri 10-30 10:00+1h0m0s", "Fri 11-27 10:00+1h0m0s"}},
		{"Outlook's last weekday", "20261030T100000", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1",
			[]string{"Fri 10-30 10:00+1h0m0s", "Mon 11-30 10:00+1h0m0s"}},
		{"15th monthly", "20261015T100000", "FREQ=MONTHLY;BYMONTHDAY=15",
			[]string{"Thu 10-15 10:00+1h0m0s", "Sun 11-15 10:00+1h0m0s"}},
		{"31st skips November", "20261031T100000", "FREQ=MONTHLY",
			[]string{"Sat 10-31 10:00+1h0m0s"}},
		{"yearly", "20251103T100000", "FREQ=YEARLY", []string{"Tue 11-03 10:00+1h0m0s"}},
		{"unsupported keeps the first", "20261026T100000", "FREQ=HOURLY", []string{"Mon 10-26 10:00+1h0m0s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ics := feed(vevent("UID:x", "DTSTART;TZID=Europe/Berlin:"+tc.dtstart, "DURATION:PT1H", "RRULE:"+tc.rrule))
			eq(t, parse(t, ics, opts("2026-10-01", "2026-12-01")), tc.want...)
		})
	}
}

func TestOldDailyMeetingReachesTheWindow(t *testing.T) {
	ics := feed(vevent("UID:x", "DTSTART:20120102T090000Z", "DURATION:PT15M", "RRULE:FREQ=DAILY"))
	got := parse(t, ics, opts("2026-10-26", "2026-10-28"))
	if len(got) != 2 {
		t.Errorf("a daily meeting since 2012: %v", got)
	}
}

func TestNotICS(t *testing.T) {
	for _, in := range []string{"", "<html>login</html>", "BEGIN:VCARD\r\nEND:VCARD\r\n"} {
		if _, err := Parse(strings.NewReader(in), opts("2026-10-26", "2026-11-01")); err == nil {
			t.Errorf("%q parsed", in)
		}
	}
	// A byte-order mark and bare LF line ends are fine.
	ok := "\xef\xbb\xbfBEGIN:VCALENDAR\nBEGIN:VEVENT\nDTSTART:20261027T080000Z\nDTEND:20261027T090000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	eq(t, parse(t, ok, opts("2026-10-26", "2026-11-01")), "Tue 10-27 09:00+1h0m0s")
}

func TestDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"PT45M": 45 * time.Minute, "PT1H30M": 90 * time.Minute, "P1D": 24 * time.Hour, "P1DT2H": 26 * time.Hour,
		"P1W": 7 * 24 * time.Hour, "PT30S": 30 * time.Second, "+PT1H": time.Hour,
	} {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Errorf("%s = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "1H", "-PT1H", "PT", "PTH", "PT1X", "P1H", "PT1"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(feed(vevent("UID:a", "DTSTART;TZID=Europe/Berlin:20261005T093000", "DURATION:PT15M",
		"RRULE:FREQ=MONTHLY;BYDAY=MO,FR;BYSETPOS=1,-1;COUNT=40", "EXDATE:20261102T083000Z")))
	f.Add(feed(vevent("DTSTART:20261027T080000Z", "DTEND:20261027T090000Z", "RRULE:FREQ=YEARLY;BYMONTH=1,6;BYDAY=-2SU")))
	f.Fuzz(func(t *testing.T, ics string) {
		o := opts("2026-01-01", "2027-01-01")
		bs, err := Parse(strings.NewReader(ics), o)
		if err != nil {
			return
		}
		for _, b := range bs {
			if !b.End.After(b.Start) || b.End.Sub(b.Start) > maxDuration || !b.End.After(o.From) || !b.Start.Before(o.To) {
				t.Fatalf("bad block %+v", b)
			}
		}
	})
}
