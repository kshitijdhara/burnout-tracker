package geometry

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

func r(x, y, w, h int) model.Rect { return model.Rect{X: x, Y: y, W: w, H: h} }

func TestVisibleFractions(t *testing.T) {
	mon := []model.Rect{r(0, 0, 100, 100)}
	two := []model.Rect{r(0, 0, 100, 100), r(100, 0, 100, 100)}
	tests := []struct {
		name     string
		windows  []model.Rect
		monitors []model.Rect
		want     []float64
	}{
		{"single full window", []model.Rect{r(0, 0, 100, 100)}, mon, []float64{1}},
		{"top covers half of bottom", []model.Rect{r(0, 0, 50, 100), r(0, 0, 100, 100)}, mon, []float64{1, 0.5}},
		{"fully covered", []model.Rect{r(0, 0, 100, 100), r(10, 10, 20, 20)}, mon, []float64{1, 0}},
		{"half off-screen", []model.Rect{r(50, 0, 100, 100)}, mon, []float64{0.5}},
		{"spans two monitors", []model.Rect{r(50, 0, 100, 100)}, two, []float64{1}},
		{"empty window", []model.Rect{r(0, 0, 0, 10)}, mon, []float64{0}},
		{"no monitors", []model.Rect{r(0, 0, 10, 10)}, nil, []float64{0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := VisibleFractions(tc.windows, tc.monitors)
			for i := range tc.want {
				if math.Abs(got[i]-tc.want[i]) > 1e-9 {
					t.Fatalf("window %d: got %v, want %v", i, got, tc.want)
				}
			}
		})
	}
}

// Visible areas can never sum to more than the monitor area, and each
// fraction stays in [0, 1].
func TestVisibleFractionsProperties(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	monitors := []model.Rect{r(0, 0, 1920, 1080), r(1920, 0, 2560, 1440)}
	monArea := 1920*1080 + 2560*1440
	for range 200 {
		n := 1 + rng.IntN(40)
		ws := make([]model.Rect, n)
		for i := range ws {
			ws[i] = r(rng.IntN(4000)-200, rng.IntN(1500)-100, rng.IntN(2000), rng.IntN(1200))
		}
		fr := VisibleFractions(ws, monitors)
		total := 0.0
		for i, f := range fr {
			if f < 0 || f > 1+1e-9 {
				t.Fatalf("fraction out of range: %v", f)
			}
			total += f * float64(ws[i].Area())
		}
		if total > float64(monArea)+1e-6 {
			t.Fatalf("visible area %v exceeds monitor area %d", total, monArea)
		}
	}
}

func TestMonitorIndexAndTopmostAt(t *testing.T) {
	two := []model.Rect{r(0, 0, 100, 100), r(100, 0, 100, 100)}
	if got := MonitorIndex(r(120, 10, 50, 50), two); got != 1 {
		t.Fatalf("MonitorIndex = %d", got)
	}
	if got := MonitorIndex(r(-500, -500, 10, 10), two); got != -1 {
		t.Fatalf("off-screen MonitorIndex = %d", got)
	}
	ws := []model.Rect{r(0, 0, 10, 10), r(0, 0, 100, 100)}
	if got := TopmostAt(ws, 5, 5); got != 0 {
		t.Fatalf("TopmostAt = %d", got)
	}
	if got := TopmostAt(ws, 50, 50); got != 1 {
		t.Fatalf("TopmostAt = %d", got)
	}
	if got := TopmostAt(ws, 500, 50); got != -1 {
		t.Fatalf("TopmostAt = %d", got)
	}
}

func BenchmarkVisibleFractions50(b *testing.B) {
	rng := rand.New(rand.NewPCG(3, 4))
	monitors := []model.Rect{r(0, 0, 1920, 1080), r(1920, 0, 2560, 1440), r(-1080, 0, 1080, 1920)}
	ws := make([]model.Rect, 50)
	for i := range ws {
		ws[i] = r(rng.IntN(4000)-1000, rng.IntN(1500), 200+rng.IntN(1500), 200+rng.IntN(1000))
	}
	b.ReportAllocs()
	for b.Loop() {
		VisibleFractions(ws, monitors)
	}
}
