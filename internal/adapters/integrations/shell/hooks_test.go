//go:build unix

package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/testsupport"
)

// runTyped types setup and commands into an interactive shell whose hook
// calls a fake emitter, and returns one record per emitter call.
func runTyped(t *testing.T, argv []string, render func(emitter, dir string) string, setup, commands []string) ([]record, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed (needed for a pseudo-terminal)")
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		t.Skipf("%s not installed", argv[0])
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	emitter := filepath.Join(dir, "fake-burntout")
	// Like the real emitter (cmd/burntout hookGuard), the fake ignores
	// SIGHUP: fish hangs up its own process group, background emitters
	// included, when it exits.
	script := "#!/bin/sh\ntrap '' HUP\nf=$(mktemp " + out + "/tmp.XXXXXX)\n" +
		"{ printf 'ARGS=%s\\n' \"$*\"; env; } > \"$f\"\n" +
		"mv \"$f\" \"$f.rec\"\n"
	if err := os.WriteFile(emitter, []byte(script), 0o700); err != nil { //nolint:gosec // a test executable
		t.Fatal(err)
	}
	hook := filepath.Join(dir, "hook")
	if err := os.WriteFile(hook, []byte(render(emitter, dir)), 0o600); err != nil {
		t.Fatal(err)
	}

	lines := append(append(append([]string(nil), setup...), "source "+hook), commands...)
	lines = append(lines, "sleep 0.3", "exit")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, append([]string{"-c", testsupport.PTYType}, argv...)...) //nolint:gosec // test tools
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TERM=dumb", "PS1=$ "}
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	output, _ := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s still running after 60s: %s", argv[0], output)
	}

	// The last command's emitter, "sleep 0.3", may still be starting.
	deadline := time.Now().Add(3 * time.Second)
	var files []string
	for {
		files, _ = filepath.Glob(filepath.Join(out, "*.rec"))
		if len(files) >= len(commands)+1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var recs []record
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // the test's own output
		if err != nil {
			t.Fatal(err)
		}
		r := record{raw: string(data), env: map[string]string{}}
		for _, line := range strings.Split(string(data), "\n") {
			if a, ok := strings.CutPrefix(line, "ARGS="); ok {
				r.args = a
			} else if k, v, ok := strings.Cut(line, "="); ok {
				r.env[k] = v
			}
		}
		recs = append(recs, r)
	}
	return recs, string(output)
}

// hookCommands are typed into every shell. Each must be recorded once,
// and the canaries must never reach the emitter.
var hookCommands = []string{
	"git status >/dev/null 2>&1",
	"printf CANARYAAA >/dev/null",
	"FOO=CANARYBBB git -C /tmp log >/dev/null 2>&1",
	"CANARYCCC123 2>/dev/null",
	" ls >/dev/null", // a leading space keeps a command out of history
	"git CANARYDDD >/dev/null 2>&1",
}

func checkHookRecords(t *testing.T, recs []record, output string, timing func(r record) bool) {
	t.Helper()
	got := byArgv0(recs)
	if len(got["git"]) != 3 || len(got["printf"]) != 1 || len(got["ls"]) != 1 || len(got["sleep"]) != 1 {
		t.Fatalf("recorded %v, want git×3, printf, ls, sleep and one unknown\n%s", keys(got), output)
	}
	if r := got[""]; len(r) != 1 || r[0].env[EnvExit] != "127" {
		t.Errorf("unknown command: want one record with an empty name and exit 127, got %v", r)
	}
	subs := map[string]bool{}
	for _, r := range got["git"] {
		subs[r.env[EnvSubcmd]] = true
	}
	if !subs["status"] || !subs["log"] || !subs[""] {
		t.Errorf("git subcommands %v, want status, log and an unlisted word dropped", subs)
	}
	for _, r := range recs {
		if r.args != "emit shell" {
			t.Fatalf("emitter argv = %q, want exactly %q", r.args, "emit shell")
		}
		if strings.Contains(r.raw, "CANARY") {
			t.Fatalf("typed text reached the emitter: %v", burntoutEnv(r))
		}
		if !timing(r) {
			t.Errorf("bad timing: %v", burntoutEnv(r))
		}
		if _, err := EmitFromEnv(func(k string) string { return r.env[k] }, "/tmp", time.Now()); err != nil {
			t.Errorf("the emitter would reject this record: %v (%v)", err, burntoutEnv(r))
		}
	}
}

// bashMajor asks a bash for its major version.
func bashMajor(t *testing.T, bash string) int {
	t.Helper()
	out, err := exec.Command(bash, "-c", "echo ${BASH_VERSINFO[0]}").Output() //nolint:gosec // test tool
	if err != nil {
		t.Fatalf("%s: %v", bash, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("%s version %q: %v", bash, out, err)
	}
	return n
}

func withClock(r record) bool {
	return r.env[EnvStart] != "" && r.env[EnvEnd] != "" && r.env[EnvDuration] == ""
}

func withDuration(r record) bool { return r.env[EnvStart] == "" && r.env[EnvDuration] != "" }

func renderBash(emitter, dir string) string {
	preexec := filepath.Join(dir, preexecName)
	if err := os.WriteFile(preexec, []byte(BashPreexec), 0o600); err != nil {
		panic(err)
	}
	return RenderBash(emitter, preexec)
}

func TestBashHook(t *testing.T) {
	bash := []string{"bash", "--norc", "--noprofile", "-i"}
	recs, out := runTyped(t, bash, renderBash, []string{"HISTCONTROL=ignorespace"}, hookCommands)
	clock := withClock
	if bashMajor(t, "bash") < 5 { // bash before 5 has no $EPOCHREALTIME
		clock = withDuration
	}
	checkHookRecords(t, recs, out, clock)

	t.Run("without a millisecond clock", func(t *testing.T) {
		// What bash 3.2 and 4 do: no $EPOCHREALTIME, so the duration is sent.
		recs, out := runTyped(t, bash, renderBash, []string{"unset EPOCHREALTIME"}, hookCommands)
		checkHookRecords(t, recs, out, withDuration)
	})
}

// macOS ships bash 3.2 as /bin/bash; run the hook on it when it differs
// from the bash on PATH.
func TestBashHookOnSystemBash(t *testing.T) {
	onPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	a, errA := filepath.EvalSymlinks(onPath)
	b, errB := filepath.EvalSymlinks("/bin/bash")
	if errA != nil || errB != nil || a == b {
		t.Skip("/bin/bash is the bash on PATH")
	}
	recs, out := runTyped(t, []string{"/bin/bash", "--norc", "--noprofile", "-i"}, renderBash, nil, hookCommands)
	clock := withClock
	if bashMajor(t, "/bin/bash") < 5 {
		clock = withDuration
	}
	checkHookRecords(t, recs, out, clock)
}

// The user's HISTCONTROL stays: bash-preexec would drop ignorespace.
func TestBashHookKeepsHistcontrol(t *testing.T) {
	bash := []string{"bash", "--norc", "--noprofile", "-i"}
	_, out := runTyped(t, bash, renderBash, []string{"HISTCONTROL=ignoreboth"}, []string{`echo "HC=[$HISTCONTROL]"`})
	if !strings.Contains(out, "HC=[ignoreboth]") {
		t.Fatalf("HISTCONTROL changed:\n%s", out)
	}
}

// Another tool that loaded bash-preexec first keeps its own hooks, and the
// library is not loaded twice.
func TestBashHookSharesBashPreexec(t *testing.T) {
	bash := []string{"bash", "--norc", "--noprofile", "-i"}
	render := func(emitter, dir string) string {
		other := "source " + filepath.Join(dir, preexecName) + "\n" +
			"other_preexec() { echo OTHER-PREEXEC-RAN; }\npreexec_functions+=(other_preexec)\n"
		return other + renderBash(emitter, dir)
	}
	recs, out := runTyped(t, bash, render, nil, []string{"true"})
	if !strings.Contains(out, "OTHER-PREEXEC-RAN") || len(byArgv0(recs)["true"]) != 1 {
		t.Fatalf("records %v; output:\n%s", keys(byArgv0(recs)), out)
	}
}

func TestFishHook(t *testing.T) {
	fish := []string{"fish", "--no-config", "-i"}
	recs, out := runTyped(t, fish, func(emitter, _ string) string { return RenderFish(emitter) }, nil, hookCommands)
	checkHookRecords(t, recs, out, withDuration)
	if strings.Contains(out, "has ended") {
		t.Errorf("fish announced the background emitter:\n%s", out)
	}
}
