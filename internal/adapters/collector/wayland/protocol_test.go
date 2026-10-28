package wayland

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtures returns the reply fixtures for protocol version v, as the
// extension of that version writes them.
func fixtures(t testing.TB, v int) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", fmt.Sprintf("v%d", v), "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures for protocol v%d: every version the agent reads needs them", v)
	}
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // G304: test fixtures
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = bytes.TrimSuffix(b, []byte("\n"))
	}
	return out
}

// TestContractVersions decodes the replies of every extension version the
// agent supports: this one (N) and the one before (N-1), which is what a
// user runs between upgrading and logging back in.
func TestContractVersions(t *testing.T) {
	if MinVersion < max(1, Version-1) {
		t.Fatalf("MinVersion %d: the agent must read N-1 = %d", MinVersion, Version-1)
	}
	for v := MinVersion; v <= Version; v++ {
		for name, line := range fixtures(t, v) {
			r, err := decodeReply(line)
			if err != nil {
				t.Errorf("v%d/%s: %v", v, name, err)
				continue
			}
			if want, _, _ := strings.Cut(strings.TrimSuffix(name, ".json"), "-"); r.Op != want {
				t.Errorf("v%d/%s: op %q, want %q", v, name, r.Op, want)
			}
			if r.V != v {
				t.Errorf("v%d/%s: v = %d", v, name, r.V)
			}
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	win := `{"id":1,"pid":1,"wm_class":"a","app":"","type":"normal","minimised":false,"on_workspace":true,"frame":{"x":0,"y":0,"w":10,"h":10}%s}`
	snap := func(windows string) string {
		return `{"v":1,"op":"snapshot","windows":[` + windows + `],"monitors":[],"focus":0,"pointer":{"x":0,"y":0}}`
	}
	tests := []struct {
		name, line string
		version    bool
	}{
		{"not json", `{`, false},
		{"array", `[]`, false},
		{"no version", `{"op":"hello"}`, true},
		{"future version", `{"v":99,"op":"hello"}`, true},
		{"unknown op", `{"v":1,"op":"dance"}`, false},
		{"snapshot without pointer", `{"v":1,"op":"snapshot","windows":[],"monitors":[]}`, false},
		{"huge coordinate", snap(`{"id":1,"pid":1,"wm_class":"","app":"","type":"normal","minimised":false,"on_workspace":true,"frame":{"x":0,"y":0,"w":99999999,"h":1}}`), false},
		{"negative size", snap(`{"id":1,"pid":1,"wm_class":"","app":"","type":"normal","minimised":false,"on_workspace":true,"frame":{"x":0,"y":0,"w":-1,"h":1}}`), false},
		{"negative pid", snap(`{"id":1,"pid":-1,"wm_class":"","app":"","type":"normal","minimised":false,"on_workspace":true,"frame":{"x":0,"y":0,"w":1,"h":1}}`), false},
		{"unknown type", snap(`{"id":1,"pid":1,"wm_class":"","app":"","type":"popup","minimised":false,"on_workspace":true,"frame":{"x":0,"y":0,"w":1,"h":1}}`), false},
		{"long class", snap(fmt.Sprintf(win, `,"wm_class":"`+strings.Repeat("a", MaxClassBytes+1)+`"`)), false},
		{"long title", snap(fmt.Sprintf(win, `,"title":"`+strings.Repeat("a", MaxTitleBytes+1)+`"`)), false},
		{"too many windows", snap(strings.TrimSuffix(strings.Repeat(fmt.Sprintf(win, "")+",", MaxWindows+1), ",")), false},
		{"long error", `{"v":1,"op":"error","error":"` + strings.Repeat("e", MaxErrorBytes+1) + `"}`, false},
	}
	for _, tc := range tests {
		_, err := decodeReply([]byte(tc.line))
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		var ve *VersionError
		if errors.As(err, &ve) != tc.version {
			t.Errorf("%s: %v, version error = %v", tc.name, err, !tc.version)
		}
	}
}

func TestDecodeAcceptsAtLimits(t *testing.T) {
	title := strings.Repeat("é", MaxTitleBytes/2) // two bytes each
	line := `{"v":1,"op":"snapshot","windows":[{"id":1,"pid":4194304,"wm_class":"","app":"","type":"dock","minimised":false,"on_workspace":true,"frame":{"x":-1048576,"y":1048576,"w":1048576,"h":0},"title":"` + title + `"}],"monitors":[],"focus":0,"pointer":{"x":0,"y":0}}`
	if _, err := decodeReply([]byte(line)); err != nil {
		t.Fatal(err)
	}
}

func TestVersionErrorAdvice(t *testing.T) {
	if msg := (&VersionError{Got: Version + 1}).Error(); !strings.Contains(msg, "update burntout") {
		t.Errorf("newer extension: %q", msg)
	}
	if msg := (&VersionError{Got: MinVersion - 1}).Error(); !strings.Contains(msg, "log out") {
		t.Errorf("older extension: %q", msg)
	}
}

// FuzzDecodeReply checks that no reply line, however malformed, panics
// the decoder or the translation after it, and that what is accepted
// stays within the contract's limits.
func FuzzDecodeReply(f *testing.F) {
	for _, line := range fixtures(f, Version) {
		f.Add(line)
	}
	f.Add([]byte(`{"v":1,"op":"snapshot","windows":null,"monitors":null,"focus":0,"pointer":{"x":0,"y":0}}`))
	f.Fuzz(func(t *testing.T, line []byte) {
		r, err := decodeReply(line)
		if err != nil || r.Op != "snapshot" {
			return
		}
		if len(r.Windows) > MaxWindows || len(r.Monitors) > MaxMonitors {
			t.Fatalf("limits not enforced: %d windows, %d monitors", len(r.Windows), len(r.Monitors))
		}
		snap := buildSnapshot(r, extra{titles: true})
		for _, w := range snap.Windows {
			if w.Visible < 0 || w.Visible > 1 {
				t.Fatalf("visible fraction %v", w.Visible)
			}
		}
	})
}
