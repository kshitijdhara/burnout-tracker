package checkin

import (
	"errors"
	"slices"
	"testing"
)

func allAnswered(in Instrument, value int) Answers {
	a := Answers{}
	for _, it := range in.Items {
		a[it.ID] = value
	}
	return a
}

func TestInstrumentsAreWellFormed(t *testing.T) {
	for _, in := range []Instrument{CBIPersonal(), CBIWork(), Daily()} {
		got, ok := ByID(in.ID)
		if !ok || got.Name != in.Name {
			t.Errorf("ByID(%q) = %+v, %v", in.ID, got, ok)
		}
		seen := map[string]bool{}
		for _, it := range in.Items {
			if seen[it.ID] || it.Text == "" || len(it.Choices) != 5 {
				t.Errorf("%s: bad item %+v", in.ID, it)
			}
			seen[it.ID] = true
			for _, c := range it.Choices {
				if c.Value < 0 || c.Value > in.Max {
					t.Errorf("%s/%s: choice %+v outside 0..%d", in.ID, it.ID, c, in.Max)
				}
			}
		}
	}
	if n := len(CBIPersonal().Items); n != 6 {
		t.Errorf("personal burnout has %d items, want 6", n)
	}
	if n := len(CBIWork().Items); n != 7 {
		t.Errorf("work-related burnout has %d items, want 7", n)
	}
}

func TestScore(t *testing.T) {
	work := CBIWork()
	// "Always" everywhere: 6 items at 100 and the reversed energy item
	// ("always enough energy") at 0.
	if s, err := work.Score(allAnswered(work, 100)); err != nil || s != 600.0/7 {
		t.Errorf("all 100: %v, %v", s, err)
	}
	a := allAnswered(work, 50)
	a["w7"] = 0 // never enough energy for family and friends: 100 after reversal
	if s, _ := work.Score(a); s != (6*50+100)/7.0 {
		t.Errorf("reversed item: %v", s)
	}
	personal := CBIPersonal()
	if s, _ := personal.Score(allAnswered(personal, 25)); s != 25 {
		t.Errorf("personal all 25: %v", s)
	}

	partial := allAnswered(work, 50)
	delete(partial, "w3")
	if _, err := work.Score(partial); !errors.Is(err, ErrIncomplete) {
		t.Errorf("partial: %v", err)
	}
	extra := allAnswered(work, 50)
	delete(extra, "w3")
	extra["x"] = 50
	if _, err := work.Score(extra); !errors.Is(err, ErrIncomplete) {
		t.Errorf("unknown item: %v", err)
	}
	bad := allAnswered(work, 50)
	bad["w1"] = 60
	if _, err := work.Score(bad); err == nil {
		t.Error("accepted a value that is not a choice")
	}
}

func TestBands(t *testing.T) {
	for score, want := range map[float64]Band{0: BandLow, 49.9: BandLow, 50: BandModerate, 74.9: BandModerate, 75: BandHigh, 99: BandHigh, 100: BandSevere} {
		if got := BandOf(score); got != want {
			t.Errorf("BandOf(%v) = %s, want %s", score, got, want)
		}
	}
	if !NeedsSupport(CBIWorkID, 75) || NeedsSupport(CBIWorkID, 74) || NeedsSupport(DailyID, 100) {
		t.Error("NeedsSupport thresholds")
	}
}

func TestResources(t *testing.T) {
	in := Resources("in")
	if len(in) != 2 || in[0].Name != "Tele-MANAS" || in[1] != Directory {
		t.Errorf("India: %+v", in)
	}
	if got := Resources("ZZ"); !slices.Equal(got, []Resource{Directory}) {
		t.Errorf("unknown region: %+v", got)
	}
	for locale, want := range map[string]string{
		"en_IN.UTF-8": "IN", "de_DE@euro": "DE", "en_GB": "GB", "C": "", "POSIX": "", "en": "", "": "", "zh_Hans_CN": "",
	} {
		if got := RegionFromLocale(locale); got != want {
			t.Errorf("RegionFromLocale(%q) = %q, want %q", locale, got, want)
		}
	}
}
