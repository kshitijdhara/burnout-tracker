// Package calendar reads busy blocks out of an iCalendar (ICS) feed
// (system design §4.7, privacy & security §4.4). It streams the feed and
// keeps, per event, only DTSTART, DTEND or DURATION, TRANSP and STATUS,
// plus RRULE, EXDATE, RECURRENCE-ID and UID to expand recurring events.
// Titles, descriptions, locations, attendees and everything else are
// discarded as they are read, never held whole. Pure: no I/O beyond the
// reader it is given, no clock.
package calendar

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Block is one busy span.
type Block struct {
	Start, End time.Time
	Tentative  bool
}

// Options bound and localise parsing.
type Options struct {
	// From and To bound the blocks returned: those overlapping [From, To).
	From, To time.Time
	// Zone resolves a TZID; nil or an error falls back to Local. Feeds
	// name IANA zones (Europe/Berlin) and sometimes Windows ones.
	Zone func(tzid string) (*time.Location, error)
	// Local is used for floating times and unknown zones.
	Local *time.Location
}

// Limits keep a hostile or broken feed from exhausting memory or time.
const (
	maxLine        = 8 << 10 // a kept property longer than this drops its event
	maxEvents      = 20_000
	maxOccurrences = 5_000  // per recurring event, inside the window
	maxIterations  = 50_000 // per recurring event, including skipped ones
	maxDuration    = 12 * time.Hour
)

// ErrNotICS means the input isn't an iCalendar feed.
var ErrNotICS = errors.New("calendar: not an iCalendar feed (no BEGIN:VCALENDAR)")

// event is what is kept of one VEVENT.
type event struct {
	uid          string
	start, end   time.Time
	duration     time.Duration
	allDay       bool
	transparent  bool
	status       string
	rrule        string
	exdates      []time.Time
	recurrenceID time.Time
	bad          bool // a kept property was unparseable or too long
}

// Parse reads busy blocks overlapping [o.From, o.To), sorted by start.
// Free (TRANSP:TRANSPARENT), cancelled, all-day and longer-than-12-hour
// events are not busy blocks: they are availability markers, holidays or
// out-of-office spans, not meetings.
func Parse(r io.Reader, o Options) ([]Block, error) {
	if o.Local == nil {
		o.Local = time.UTC
	}
	lr := newLineReader(r)
	var (
		events       []*event
		cur          *event
		depth        int // nesting inside the current VEVENT (VALARM, …)
		sawCalendar  bool
		sawAnyLine   bool
		calendarDone bool
	)
	for {
		name, params, value, tooLong, err := lr.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("calendar: %w", err)
		}
		if !sawAnyLine {
			sawAnyLine = true
			if name != "BEGIN" || !strings.EqualFold(value, "VCALENDAR") {
				return nil, ErrNotICS
			}
		}
		switch {
		case name == "BEGIN" && strings.EqualFold(value, "VCALENDAR"):
			sawCalendar = true
		case name == "END" && strings.EqualFold(value, "VCALENDAR"):
			calendarDone = true
		case name == "BEGIN" && cur == nil && strings.EqualFold(value, "VEVENT"):
			cur, depth = &event{}, 0
		case name == "BEGIN" && cur != nil:
			depth++
		case name == "END" && cur != nil && depth > 0:
			depth--
		case name == "END" && cur != nil && strings.EqualFold(value, "VEVENT"):
			if !cur.bad && !cur.start.IsZero() {
				if len(events) >= maxEvents {
					return nil, fmt.Errorf("calendar: more than %d events", maxEvents)
				}
				events = append(events, cur)
			}
			cur = nil
		case cur != nil && depth == 0:
			if tooLong {
				if kept(name) {
					cur.bad = true
				}
				continue
			}
			cur.set(name, params, value, o)
		}
		if calendarDone {
			break
		}
	}
	if !sawCalendar {
		return nil, ErrNotICS
	}
	return expand(events, o), nil
}

// kept reports whether a property is one the parser keeps.
func kept(name string) bool {
	switch name {
	case "UID", "DTSTART", "DTEND", "DURATION", "TRANSP", "STATUS", "RRULE", "EXDATE", "RECURRENCE-ID":
		return true
	}
	return false
}

func (e *event) set(name string, params map[string]string, value string, o Options) {
	switch name {
	case "UID":
		e.uid = value
	case "DTSTART":
		t, allDay, err := parseTime(value, params, o)
		e.start, e.allDay, e.bad = t, allDay, e.bad || err != nil
	case "DTEND":
		t, _, err := parseTime(value, params, o)
		e.end, e.bad = t, e.bad || err != nil
	case "DURATION":
		d, err := parseDuration(value)
		e.duration, e.bad = d, e.bad || err != nil
	case "TRANSP":
		e.transparent = strings.EqualFold(value, "TRANSPARENT")
	case "STATUS":
		e.status = strings.ToUpper(value)
	case "RRULE":
		e.rrule = value
	case "EXDATE":
		for v := range strings.SplitSeq(value, ",") {
			if t, _, err := parseTime(v, params, o); err == nil {
				e.exdates = append(e.exdates, t)
			}
		}
	case "RECURRENCE-ID":
		t, _, err := parseTime(value, params, o)
		e.recurrenceID, e.bad = t, e.bad || err != nil
	}
	// Every other property (SUMMARY, DESCRIPTION, LOCATION, ATTENDEE,
	// ORGANIZER, …) is dropped here, unread.
}

// length is the event's duration.
func (e *event) length() time.Duration {
	switch {
	case !e.end.IsZero():
		return e.end.Sub(e.start)
	case e.duration > 0:
		return e.duration
	}
	return 0
}

// busy reports whether the event (or an override) blocks time.
func (e *event) busy() bool {
	d := e.length()
	return !e.allDay && !e.transparent && e.status != "CANCELLED" && d > 0 && d <= maxDuration
}

func expand(events []*event, o Options) []Block {
	// Overrides replace one instance of a recurring event (same UID, the
	// instance's original start).
	type instance struct {
		uid string
		at  int64
	}
	overrides := map[instance]*event{}
	for _, e := range events {
		if !e.recurrenceID.IsZero() && e.uid != "" {
			overrides[instance{e.uid, e.recurrenceID.Unix()}] = e
		}
	}
	var out []Block
	add := func(e *event, start time.Time) {
		if !e.busy() {
			return
		}
		end := start.Add(e.length())
		if end.After(o.From) && start.Before(o.To) {
			out = append(out, Block{Start: start, End: end, Tentative: e.status == "TENTATIVE"})
		}
	}
	for _, e := range events {
		if !e.recurrenceID.IsZero() {
			add(e, e.start) // the override itself, at its own time
			continue
		}
		if e.rrule == "" {
			add(e, e.start)
			continue
		}
		rule, err := parseRule(e.rrule, e.start.Location(), o)
		if err != nil {
			add(e, e.start) // an unsupported rule keeps its first instance
			continue
		}
		excluded := make(map[int64]bool, len(e.exdates))
		for _, t := range e.exdates {
			excluded[t.Unix()] = true
		}
		rule.each(e.start, o.From.Add(-e.length()), o.To, func(t time.Time) {
			if excluded[t.Unix()] {
				return
			}
			if _, overridden := overrides[instance{e.uid, t.Unix()}]; overridden && e.uid != "" {
				return
			}
			add(e, t)
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].End.Before(out[j].End)
	})
	return out
}

// parseTime reads a DATE or DATE-TIME value with its TZID parameter.
func parseTime(value string, params map[string]string, o Options) (time.Time, bool, error) {
	value = strings.TrimSpace(value)
	if params["VALUE"] == "DATE" || len(value) == 8 {
		t, err := time.ParseInLocation("20060102", value, o.Local)
		return t, true, err
	}
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse("20060102T150405Z", value)
		return t, false, err
	}
	loc := o.Local
	if tzid := strings.Trim(params["TZID"], `"`); tzid != "" && o.Zone != nil {
		if z, err := o.Zone(tzid); err == nil && z != nil {
			loc = z
		}
	}
	t, err := time.ParseInLocation("20060102T150405", value, loc)
	return t, false, err
}

// parseDuration reads an RFC 5545 duration (P1W, PT1H30M, P1DT2H).
func parseDuration(v string) (time.Duration, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "+")
	if strings.HasPrefix(v, "-") || !strings.HasPrefix(v, "P") {
		return 0, fmt.Errorf("duration %q", v)
	}
	var d time.Duration
	n, units := 0, 0
	digits := false
	inTime := false
	for _, c := range v[1:] {
		switch {
		case c >= '0' && c <= '9':
			n = n*10 + int(c-'0')
			digits = true
			if n > 1_000_000 {
				return 0, fmt.Errorf("duration %q", v)
			}
			continue
		case c == 'T':
			inTime = true
			continue
		}
		if !digits {
			return 0, fmt.Errorf("duration %q", v)
		}
		unit := map[rune]time.Duration{'W': 7 * 24 * time.Hour, 'D': 24 * time.Hour}
		if inTime {
			unit = map[rune]time.Duration{'H': time.Hour, 'M': time.Minute, 'S': time.Second}
		}
		u, ok := unit[c]
		if !ok {
			return 0, fmt.Errorf("duration %q", v)
		}
		d += time.Duration(n) * u
		n, digits = 0, false
		units++
	}
	if digits || units == 0 {
		return 0, fmt.Errorf("duration %q", v)
	}
	return d, nil
}

// lineReader unfolds content lines (RFC 5545 §3.1) and splits them into
// name, parameters and value. A logical line longer than maxLine is read
// through without being kept, and reported as tooLong.
type lineReader struct {
	r *bufio.Reader
}

func newLineReader(r io.Reader) *lineReader {
	br := bufio.NewReaderSize(r, 16<<10)
	if b, err := br.Peek(3); err == nil && string(b) == "\xef\xbb\xbf" {
		_, _ = br.Discard(3) // a UTF-8 byte-order mark
	}
	return &lineReader{r: br}
}

func (l *lineReader) next() (name string, params map[string]string, value string, tooLong bool, err error) {
	for {
		var buf bytes.Buffer
		over := false
		for first := true; ; first = false {
			if !first {
				// A continuation starts with one space or tab, which is removed.
				b, err := l.r.Peek(1)
				if err != nil || (b[0] != ' ' && b[0] != '\t') {
					break
				}
				_, _ = l.r.ReadByte()
			}
			if err := l.physical(&buf, &over); err != nil {
				if errors.Is(err, io.EOF) && (buf.Len() > 0 || over) {
					break
				}
				return "", nil, "", false, err
			}
		}
		line := buf.String()
		if strings.TrimSpace(line) == "" && !over {
			continue
		}
		name, params, value = split(line)
		return name, params, value, over, nil
	}
}

// physical reads one physical line (to LF) into buf, stopping at maxLine
// and discarding the rest.
func (l *lineReader) physical(buf *bytes.Buffer, over *bool) error {
	read := false
	for {
		chunk, err := l.r.ReadSlice('\n')
		if len(chunk) > 0 {
			read = true
		}
		if !*over {
			if buf.Len()+len(chunk) > maxLine {
				*over = true
				// Keep the start: the property name is still needed.
				keep := max(0, min(len(chunk), maxLine-buf.Len()))
				buf.Write(chunk[:keep])
			} else {
				buf.Write(chunk)
			}
		}
		switch {
		case err == nil:
			trimEOL(buf)
			return nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && read:
			trimEOL(buf)
			return nil
		default:
			return err
		}
	}
}

func trimEOL(buf *bytes.Buffer) {
	b := buf.Bytes()
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	buf.Truncate(len(b))
}

// split separates NAME;PARAM=V;PARAM="V":VALUE. Names and parameter names
// are upper-cased.
func split(line string) (string, map[string]string, string) {
	inQuote := false
	colon := -1
	for i, c := range line {
		if c == '"' {
			inQuote = !inQuote
		} else if c == ':' && !inQuote {
			colon = i
			break
		}
	}
	if colon < 0 {
		return strings.ToUpper(line), nil, ""
	}
	head, value := line[:colon], line[colon+1:]
	parts := strings.Split(head, ";")
	var params map[string]string
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		if params == nil {
			params = map[string]string{}
		}
		params[strings.ToUpper(k)] = v
	}
	return strings.ToUpper(parts[0]), params, value
}
