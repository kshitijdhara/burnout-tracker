package dashboard

// The routes that change data (privacy & security §5.1, §8.2; system
// design §9): pause and resume, forget the last hour or today,
// annotations, boundaries, and the weekly check-in. guard lets a POST or DELETE
// through only if it matches a route here, comes from the page itself and
// carries the session's CSRF token.

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/annotation"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/boundary"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/checkin"
)

// csrfHeader carries the session's CSRF token on every write.
const csrfHeader = "X-CSRF-Token"

// maxBody caps a write's request body; the largest, a check-in, is well
// under 1 KiB.
const maxBody = 8 << 10

// writeTimeout bounds a write's response. Forgetting waits for the daemon
// for up to 30 s, longer than the server's usual write timeout.
const writeTimeout = 45 * time.Second

// Pause lengths a write may ask for.
const (
	PauseHour     = "1h"
	PauseTomorrow = "tomorrow"
	PauseResumed  = "until-resumed"
)

// Ranges a write may forget.
const (
	ForgetHour  = "hour"
	ForgetToday = "today"
)

// Control changes the user's data. Each method is called per request and
// must be safe for concurrent use. The checks that the request is the
// user's own are done before any method is called; the arguments are
// already validated.
type Control interface {
	Pause(ctx context.Context, length string) error // PauseHour, PauseTomorrow or PauseResumed
	Resume(ctx context.Context) error
	Forget(ctx context.Context, span string) error // ForgetHour or ForgetToday
	Annotations(ctx context.Context) ([]Annotation, error)
	AddAnnotation(ctx context.Context, a annotation.Annotation) (int64, error)
	// RemoveAnnotation reports whether there was such an annotation.
	RemoveAnnotation(ctx context.Context, id int64) (bool, error)
	SaveCheckins(ctx context.Context, rs []checkin.Record) error
	Boundaries(ctx context.Context) ([]Boundary, error) // the active ones
	// SetBoundary makes b, already parsed, the rule of its kind from
	// tomorrow; changed is false when it was already the rule.
	SetBoundary(ctx context.Context, b boundary.Boundary) (id int64, changed bool, err error)
	// RemoveBoundary reports whether there was such an active boundary.
	RemoveBoundary(ctx context.Context, id int64) (bool, error)
	// Region is the user's ISO 3166 region for support lines, or "".
	Region() string
}

// Annotation is a marked stretch of days, as the page lists it.
type Annotation struct {
	ID   int64  `json:"id"`
	Text string `json:"text"` // "vacation, 2026-11-02 to 2026-11-06 (5 days): note"
}

// Boundary is an active rule, as the page lists it.
type Boundary struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Text  string `json:"text"` // "stop work by 21:30 on workdays"
	Since string `json:"since"`
}

// Session is what the page needs before it changes anything.
type Session struct {
	CSRF     string `json:"csrf"`
	Writable bool   `json:"writable"`
}

// Instrument is a check-in questionnaire, for the form.
type Instrument struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Intro string     `json:"intro"`
	Items []Question `json:"items"`
}

// Question is one check-in item.
type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Choices []Choice `json:"choices"`
}

// Choice is one answer to a question.
type Choice struct {
	Label string `json:"label"`
	Value int    `json:"value"`
}

// CheckinResult is a saved check-in's scores and, when a score is high,
// the support message and lines (system design §8.4).
type CheckinResult struct {
	Scores    []Check    `json:"scores"`
	Support   bool       `json:"support"`
	Message   string     `json:"message,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
}

// Resource is a support line.
type Resource struct {
	Name    string `json:"name"`
	Contact string `json:"contact"`
}

var (
	errNotFound  = errors.New("not found")
	errWritesOff = errors.New("writes off")
)

// badRequest is a refusal whose text is safe to show: it names nothing
// but the request.
type badRequest string

func (b badRequest) Error() string { return string(b) }

func (s *Server) writeRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/pause", s.write(func(r *http.Request) (any, error) {
		var body struct {
			For string `json:"for"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		switch body.For {
		case PauseHour, PauseTomorrow, PauseResumed:
		default:
			return nil, badRequest("pause for 1h, tomorrow or until-resumed")
		}
		return nil, s.ctl.Pause(r.Context(), body.For)
	}))
	mux.HandleFunc("POST /api/resume", s.write(func(r *http.Request) (any, error) {
		if err := decode(r, &struct{}{}); err != nil {
			return nil, err
		}
		return nil, s.ctl.Resume(r.Context())
	}))
	mux.HandleFunc("DELETE /api/data", s.write(func(r *http.Request) (any, error) {
		span := r.URL.Query().Get("range")
		if span != ForgetHour && span != ForgetToday {
			return nil, badRequest("range must be hour or today")
		}
		return nil, s.ctl.Forget(r.Context(), span)
	}))
	mux.HandleFunc("POST /api/annotations", s.write(func(r *http.Request) (any, error) {
		var body struct {
			Kind string `json:"kind"`
			From string `json:"from"`
			To   string `json:"to"`
			Note string `json:"note"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		a := annotation.Annotation{Kind: annotation.Kind(body.Kind), From: body.From, To: body.To, Note: body.Note}
		if a.To == "" {
			a.To = a.From
		}
		if err := a.Validate(); err != nil {
			return nil, badRequest(err.Error()) // the domain's own words, about the request only
		}
		id, err := s.ctl.AddAnnotation(r.Context(), a)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"id": id}, nil
	}))
	mux.HandleFunc("DELETE /api/annotations/{id}", s.write(func(r *http.Request) (any, error) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			return nil, badRequest("no such annotation")
		}
		ok, err := s.ctl.RemoveAnnotation(r.Context(), id)
		if err == nil && !ok {
			err = errNotFound
		}
		return nil, err
	}))
	mux.HandleFunc("POST /api/boundaries", s.write(func(r *http.Request) (any, error) {
		var body struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
			// FromReview marks a boundary set from the week's
			// suggestions (system design §8.6).
			FromReview bool `json:"from_review"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		b, err := boundary.Parse(body.Kind, body.Value)
		if err != nil {
			return nil, badRequest(err.Error()) // the domain's own words, about the request only
		}
		b.Source = boundary.SourceDashboard
		if body.FromReview {
			b.Source = boundary.SourceReview
		}
		id, changed, err := s.ctl.SetBoundary(r.Context(), b)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "changed": changed}, nil
	}))
	mux.HandleFunc("DELETE /api/boundaries/{id}", s.write(func(r *http.Request) (any, error) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			return nil, badRequest("no such boundary")
		}
		ok, err := s.ctl.RemoveBoundary(r.Context(), id)
		if err == nil && !ok {
			err = errNotFound
		}
		return nil, err
	}))
	mux.HandleFunc("POST /api/checkins", s.write(s.checkin))
	return mux
}

// write is api for a route that changes data: it refuses when there is no
// Control, checks the body's type and size, and allows the response
// longer.
func (s *Server) write(fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(writeTimeout))
		s.api(func(r *http.Request) (any, error) {
			if s.ctl == nil {
				return nil, errWritesOff
			}
			if r.Method == http.MethodPost {
				if t, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || t != "application/json" {
					return nil, badRequest("send JSON")
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			return fn(r)
		})(w, r)
	}
}

// decode reads a JSON body into v, strictly.
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return badRequest("couldn't read the request")
	}
	if d.More() {
		return badRequest("couldn't read the request")
	}
	return nil
}

func (s *Server) session(r *http.Request) (any, error) {
	tok, _ := s.sessionToken(r) // guard checked there is one
	return Session{CSRF: tok, Writable: s.ctl != nil}, nil
}

func (s *Server) boundaries(r *http.Request) (any, error) {
	if s.ctl == nil {
		return []Boundary{}, nil
	}
	bs, err := s.ctl.Boundaries(r.Context())
	if bs == nil {
		bs = []Boundary{}
	}
	return bs, err
}

func (s *Server) annotations(r *http.Request) (any, error) {
	if s.ctl == nil {
		return []Annotation{}, nil
	}
	as, err := s.ctl.Annotations(r.Context())
	if as == nil {
		as = []Annotation{}
	}
	return as, err
}

// weeklyCheckin is the weekly check-in's questions, in the instruments'
// own validated wording.
func weeklyCheckin() []Instrument {
	var out []Instrument
	for _, inst := range checkin.Weekly() {
		in := Instrument{ID: inst.ID, Name: inst.Name, Intro: inst.Intro}
		for _, it := range inst.Items {
			q := Question{ID: it.ID, Text: it.Text}
			for _, c := range it.Choices {
				q.Choices = append(q.Choices, Choice{Label: c.Label, Value: c.Value})
			}
			in.Items = append(in.Items, q)
		}
		out = append(out, in)
	}
	return out
}

// checkin saves a weekly check-in: every item of both CBI subscales,
// scored here as the CLI scores it. A partial check-in isn't stored.
func (s *Server) checkin(r *http.Request) (any, error) {
	var body struct {
		Answers map[string]checkin.Answers `json:"answers"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	weekly := checkin.Weekly()
	if len(body.Answers) != len(weekly) {
		return nil, badRequest("answer every question of the weekly check-in")
	}
	now := s.now()
	var records []checkin.Record
	res := CheckinResult{Scores: []Check{}}
	for _, inst := range weekly {
		score, err := inst.Score(body.Answers[inst.ID])
		if err != nil {
			return nil, badRequest("answer every question of the weekly check-in")
		}
		records = append(records, checkin.Record{At: now, Instrument: inst.ID, Answers: body.Answers[inst.ID], Score: score})
		res.Scores = append(res.Scores, Check{Name: inst.Name, Score: score, Band: string(checkin.BandOf(score))})
		res.Support = res.Support || checkin.NeedsSupport(inst.ID, score)
	}
	if err := s.ctl.SaveCheckins(r.Context(), records); err != nil {
		return nil, err
	}
	if res.Support {
		res.Message = checkin.SupportMessage
		for _, l := range checkin.Resources(s.ctl.Region()) {
			res.Resources = append(res.Resources, Resource{Name: l.Name, Contact: l.Contact})
		}
	}
	return res, nil
}
