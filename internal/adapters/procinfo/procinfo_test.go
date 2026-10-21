package procinfo

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

var _ ports.ProcessInfo = Info{}

func TestAncestorsLogic(t *testing.T) {
	tree := map[int]int{10: 9, 9: 8, 8: 1, 20: 21, 21: 20, 30: 31}
	parent := func(pid int) (int, error) {
		if p, ok := tree[pid]; ok {
			return p, nil
		}
		return 0, ErrNoProcess
	}
	tests := []struct {
		pid, limit int
		want       []int
		wantErr    bool
	}{
		{10, 10, []int{9, 8, 1}, false},
		{10, 2, []int{9, 8}, false},
		{20, 10, []int{21}, false}, // cycle stops
		{30, 10, []int{31}, false}, // chain breaks above
		{99, 10, nil, true},        // unknown pid
		{0, 10, nil, true},
	}
	for _, tc := range tests {
		got, err := ancestors(tc.pid, tc.limit, parent)
		if (err != nil) != tc.wantErr {
			t.Errorf("ancestors(%d) err = %v", tc.pid, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("ancestors(%d, %d) = %v, want %v", tc.pid, tc.limit, got, tc.want)
		}
	}
}

func TestRealProcess(t *testing.T) {
	var info Info
	anc, err := info.Ancestors(os.Getpid(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(anc) == 0 || anc[0] != os.Getppid() {
		t.Fatalf("ancestors = %v, want first %d", anc, os.Getppid())
	}
	name, err := info.Name(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if name == "" {
		t.Fatal("empty process name")
	}
	if _, err := info.Name(-1); !errors.Is(err, ErrNoProcess) {
		t.Fatalf("Name(-1) = %v", err)
	}
}

func TestLineage(t *testing.T) {
	var info Info
	lin, err := info.Lineage(os.Getpid(), 16)
	if err != nil {
		t.Fatal(err)
	}
	anc, err := info.Ancestors(os.Getpid(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(lin) == 0 || lin[0].PID != os.Getppid() || lin[0].Name == "" {
		t.Fatalf("lineage = %+v", lin)
	}
	if len(lin) != len(anc) {
		t.Fatalf("lineage %d entries, ancestors %d", len(lin), len(anc))
	}
	if _, err := info.Lineage(0, 4); !errors.Is(err, ErrNoProcess) {
		t.Fatalf("Lineage(0) = %v", err)
	}
}
