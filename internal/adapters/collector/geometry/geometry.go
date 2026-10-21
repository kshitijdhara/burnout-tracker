// Package geometry computes how much of each window is actually visible,
// given the windows' stacking order and the active monitors. It is pure Go
// so every platform collector shares one tested implementation.
package geometry

import (
	"slices"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// VisibleFractions returns, for each window, the fraction of its area that
// is on an active monitor and not covered by a window above it.
//
// windows must be in z-order, topmost first. The result has the same length
// and order. Empty windows get 0.
//
// The algorithm compresses all rectangle edges into a grid and finds, for
// each grid cell, the topmost window covering it. With n windows that is at
// most (4n+2m)^2 cells × n windows per call, well under a millisecond for
// the 50-window stress case.
func VisibleFractions(windows, monitors []model.Rect) []float64 {
	out := make([]float64, len(windows))
	if len(windows) == 0 || len(monitors) == 0 {
		return out
	}

	xs := make([]int, 0, 2*(len(windows)+len(monitors)))
	ys := make([]int, 0, cap(xs))
	for _, r := range slices.Concat(windows, monitors) {
		if r.Area() == 0 {
			continue
		}
		xs = append(xs, r.X, r.X+r.W)
		ys = append(ys, r.Y, r.Y+r.H)
	}
	xs = sortedUnique(xs)
	ys = sortedUnique(ys)

	visible := make([]int, len(windows))
	for i := 0; i+1 < len(xs); i++ {
		cx0, cx1 := xs[i], xs[i+1]
		for j := 0; j+1 < len(ys); j++ {
			cy0, cy1 := ys[j], ys[j+1]
			if !anyCovers(monitors, cx0, cy0, cx1, cy1) {
				continue
			}
			for w, r := range windows {
				if r.Area() > 0 && covers(r, cx0, cy0, cx1, cy1) {
					visible[w] += (cx1 - cx0) * (cy1 - cy0)
					break // only the topmost window owns this cell
				}
			}
		}
	}
	for i, r := range windows {
		if a := r.Area(); a > 0 {
			out[i] = float64(visible[i]) / float64(a)
		}
	}
	return out
}

// MonitorIndex returns the index of the monitor that overlaps r the most,
// or -1 if r is entirely off-screen.
func MonitorIndex(r model.Rect, monitors []model.Rect) int {
	best, bestArea := -1, 0
	for i, m := range monitors {
		if a := intersectArea(r, m); a > bestArea {
			best, bestArea = i, a
		}
	}
	return best
}

// TopmostAt returns the index of the topmost window containing point (x, y),
// or -1. windows must be in z-order, topmost first.
func TopmostAt(windows []model.Rect, x, y int) int {
	for i, r := range windows {
		if r.Area() > 0 && x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
			return i
		}
	}
	return -1
}

func covers(r model.Rect, x0, y0, x1, y1 int) bool {
	return r.X <= x0 && r.Y <= y0 && r.X+r.W >= x1 && r.Y+r.H >= y1
}

func anyCovers(rs []model.Rect, x0, y0, x1, y1 int) bool {
	for _, r := range rs {
		if covers(r, x0, y0, x1, y1) {
			return true
		}
	}
	return false
}

func intersectArea(a, b model.Rect) int {
	w := min(a.X+a.W, b.X+b.W) - max(a.X, b.X)
	h := min(a.Y+a.H, b.Y+b.H) - max(a.Y, b.Y)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

func sortedUnique(v []int) []int {
	slices.Sort(v)
	return slices.Compact(v)
}
