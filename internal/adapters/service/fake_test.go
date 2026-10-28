package service

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner records commands and answers from a script.
type fakeRunner struct {
	calls []string
	out   map[string]string // command → output
	fail  map[string]bool   // command → always fails
	failN map[string]int    // command → fails this many more times
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	c := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, c)
	if f.fail[c] {
		return f.out[c], errors.New("exit status 1")
	}
	if f.failN[c] > 0 {
		f.failN[c]--
		return f.out[c], errors.New("exit status 1")
	}
	return f.out[c], nil
}

var testArgs = []string{"daemon", "--service"}

func manager(t *testing.T, goos string, r *fakeRunner) Manager {
	home := t.TempDir()
	return Manager{
		GOOS: goos, Bin: "/Users/me/.local/bin/burntout", Args: testArgs,
		Env:  map[string]string{"BURNTOUT_DATA_DIR": "/Users/me/data"},
		Home: home, Config: filepath.Join(home, ".config"), LogDir: filepath.Join(home, "data", "logs"),
		UID: 501, Run: r, Sleep: func(time.Duration) {},
	}
}
