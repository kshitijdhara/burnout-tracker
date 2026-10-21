package shell

import (
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestEmitFromEnv(t *testing.T) {
	now := time.UnixMilli(1792542700000)
	base := map[string]string{
		EnvArgv0: "git", EnvSubcmd: "status", EnvExit: "1",
		EnvStart: "1792542612.5852947235", EnvEnd: "1792542613.0001",
		EnvTTY: "/dev/pts/3", EnvShellPID: "4242",
	}
	m, err := EmitFromEnv(envOf(base), "/home/u/proj", now)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Shell
	if s.Argv0 != "git" || s.Subcmd != "status" || s.Exit != 1 || s.ShellPID != 4242 || s.TTY != "/dev/pts/3" {
		t.Fatalf("got %+v", s)
	}
	if s.StartMs != 1792542612585 || s.EndMs != 1792542613000 {
		t.Fatalf("times = %d..%d", s.StartMs, s.EndMs)
	}
	if s.Cwd != "/home/u/proj" || m.SentMs != now.UnixMilli() {
		t.Fatalf("got %+v", m)
	}
}

func TestEmitFromEnvDropsBadNames(t *testing.T) {
	tests := []struct {
		name, argv0, sub, wantArgv0, wantSub string
	}{
		{"bad subcommand only", "git", "x=y", "git", ""},
		{"bad argv0 drops both", "has space", "status", "", ""},
		{"empty argv0 drops subcommand", "", "status", "", ""},
		{"long token", "ghp_0123456789012345678901234567890123456789012345678901234567890123", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := EmitFromEnv(envOf(map[string]string{
				EnvArgv0: tc.argv0, EnvSubcmd: tc.sub, EnvExit: "0",
				EnvStart: "1792542612.5", EnvEnd: "1792542612.6",
			}), "/", time.UnixMilli(1))
			if err != nil {
				t.Fatal(err)
			}
			if m.Shell.Argv0 != tc.wantArgv0 || m.Shell.Subcmd != tc.wantSub {
				t.Fatalf("got argv0=%q sub=%q", m.Shell.Argv0, m.Shell.Subcmd)
			}
		})
	}
}

func TestEmitFromEnvRejectsBadTimes(t *testing.T) {
	for _, env := range []map[string]string{
		{EnvExit: "0", EnvStart: "", EnvEnd: "1.0"},
		{EnvExit: "0", EnvStart: "abc", EnvEnd: "1.0"},
		{EnvExit: "x", EnvStart: "1792542612.5", EnvEnd: "1792542612.6"},
	} {
		if _, err := EmitFromEnv(envOf(env), "/", time.UnixMilli(1)); err == nil {
			t.Fatalf("accepted %v", env)
		}
	}
}

func TestEpochMillis(t *testing.T) {
	tests := map[string]int64{
		"1792542612.5852947235": 1792542612585,
		"1792542612,5":          1792542612500,
		"1792542612":            1792542612000,
	}
	for in, want := range tests {
		got, err := epochMillis(in)
		if err != nil || got != want {
			t.Errorf("epochMillis(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
