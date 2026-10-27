//go:build unix

package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// zshDeadline bounds each zsh run in these tests.
const zshDeadline = 30 * time.Second

// record is what the fake emitter captured for one command.
type record struct {
	args string
	env  map[string]string
	raw  string
}

// runZsh drives an interactive zsh (reading commands from stdin, which
// still runs preexec/precmd) with the rendered hook and a fake emitter, and
// returns one record per emitter invocation and the shell's output.
func runZsh(t *testing.T, interactive bool, commands []string, wantRecords int) ([]record, string) {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	// Each invocation writes its argv and environment to its own temp file
	// and renames it into place when complete, so concurrent background
	// emitters don't interleave and the test never reads a half-written
	// record (mktemp creates the file before its contents exist).
	emitter := filepath.Join(dir, "fake-burnout")
	script := "#!/bin/sh\nf=$(mktemp " + out + "/tmp.XXXXXX)\n" +
		"{ printf 'ARGS=%s\\n' \"$*\"; env; } > \"$f\"\n" +
		"mv \"$f\" \"$f.rec\"\n"
	if err := os.WriteFile(emitter, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(dir, "burnout.zsh")
	if err := os.WriteFile(hook, []byte(RenderZsh(emitter)), 0o600); err != nil {
		t.Fatal(err)
	}

	// A deadline turns a zsh that waits on input into a fast failure
	// instead of a hang until go test's own timeout.
	ctx, cancel := context.WithTimeout(t.Context(), zshDeadline)
	defer cancel()
	var cmd *exec.Cmd
	if interactive {
		cmd = exec.CommandContext(ctx, "zsh", "-f", "-i")
		// An interactive zsh reads from /dev/tty when it has one, which would
		// take over the developer's terminal. A new session has no
		// controlling terminal, so zsh reads the script from stdin instead.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Stdin = strings.NewReader("source '" + hook + "'\n" + strings.Join(commands, "\n") + "\n")
	} else {
		cmd = exec.CommandContext(ctx, "zsh", "-f", "-c", "source '"+hook+"'; "+strings.Join(commands, "; "))
	}
	cmd.WaitDelay = time.Second
	cmd.Dir = dir
	// A minimal environment keeps failure output small and free of the
	// test runner's own variables.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TERM=dumb", "PS1=", "PROMPT=", "RPROMPT="}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("zsh still running after %s (waiting on a terminal?): %s", zshDeadline, output)
	}
	if err != nil && interactive {
		t.Logf("zsh output: %s", output)
	}

	deadline := time.Now().Add(3 * time.Second)
	var files []string
	for {
		files, _ = filepath.Glob(filepath.Join(out, "*.rec"))
		if len(files) >= wantRecords || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if wantRecords == 0 {
		time.Sleep(300 * time.Millisecond) // give a wrongly fired emitter time to show up
		files, _ = filepath.Glob(filepath.Join(out, "*.rec"))
	}
	var recs []record
	for _, f := range files {
		data, err := os.ReadFile(f)
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

func byArgv0(recs []record) map[string][]record {
	m := map[string][]record{}
	for _, r := range recs {
		m[r.env[EnvArgv0]] = append(m[r.env[EnvArgv0]], r)
	}
	return m
}

// TestZshHookNeverLeaksCommandLine is the ps-leak guarantee from
// docs/privacy-and-security.md §12: nothing typed reaches the emitter's
// argv, and only allowlisted, resolvable names reach its environment.
func TestZshHookNeverLeaksCommandLine(t *testing.T) {
	commands := []string{
		"printf 'CANARYAAA' >/dev/null",
		"CANARYBBB123",
		"git -c x=y status >/dev/null 2>&1",
		"FOO=CANARYCCC env -i true",
		"git -C /tmp CANARYDDD >/dev/null 2>&1",
	}
	recs, _ := runZsh(t, true, commands, len(commands))
	if len(recs) != len(commands) {
		t.Fatalf("got %d emitter records, want %d: %+v", len(recs), len(commands), recs)
	}
	for _, r := range recs {
		if r.args != "emit shell" {
			t.Fatalf("emitter argv = %q, want exactly %q", r.args, "emit shell")
		}
		if strings.Contains(r.raw, "CANARY") {
			t.Fatalf("canary reached the emitter: args=%q env=%v", r.args, burnoutEnv(r))
		}
		if r.env[EnvStart] == "" || r.env[EnvEnd] == "" || r.env[EnvExit] == "" {
			t.Fatalf("missing timing/exit env: %v", r.env)
		}
	}

	got := byArgv0(recs)
	if len(got["printf"]) != 1 {
		t.Errorf("printf not recorded: %v", keys(got))
	}
	if r := got[""]; len(r) != 1 || r[0].env[EnvExit] != "127" {
		t.Errorf("unknown command should be recorded with empty argv0 and exit 127: %v", keys(got))
	}
	if len(got["true"]) != 1 {
		t.Errorf("env prefix not skipped: %v", keys(got))
	}
	gits := got["git"]
	if len(gits) != 2 {
		t.Fatalf("git commands = %d: %v", len(gits), keys(got))
	}
	subs := map[string]bool{}
	for _, r := range gits {
		subs[r.env[EnvSubcmd]] = true
	}
	// -c x=y is skipped and status found; the unlisted word after -C dir is
	// dropped inside the shell, so its record has no subcommand.
	if !subs["status"] || !subs[""] {
		t.Errorf("git subcommands = %v, want status and empty", subs)
	}
}

func burnoutEnv(r record) map[string]string {
	out := map[string]string{}
	for k, v := range r.env {
		if strings.HasPrefix(k, "BURNOUT_") {
			out[k] = v
		}
	}
	return out
}

func keys(m map[string][]record) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestZshHookSilentWhenNotInteractive(t *testing.T) {
	recs, _ := runZsh(t, false, []string{"true", "printf x >/dev/null"}, 0)
	if len(recs) != 0 {
		t.Fatalf("non-interactive shell emitted %d records", len(recs))
	}
}

// TestZshHookResolvesLikeZsh checks the hook names the command zsh would
// actually run: an unquoted ~/ path resolves, a quoted one does not; the
// shell's prefix words (!, coproc, repeat N, grouping) and sudo's and
// env's options, with their values, are skipped to the real command; reserved words are never recorded as commands. It also pins
// that the hook prints nothing: it once evaluated the typed word as
// arithmetic, printing "bad math expression" before ~/ commands.
func TestZshHookResolvesLikeZsh(t *testing.T) {
	commands := []string{
		"mkdir -p ~/bin && printf '#!/bin/sh\\n' >~/bin/tool && chmod +x ~/bin/tool",
		"~/bin/tool",
		"'~/bin/tool' 2>/dev/null",
		"~/CANARYEEE 2>/dev/null",
		"! git status >/dev/null 2>&1",
		"repeat 2 git status >/dev/null 2>&1",
		"{ git status } >/dev/null 2>&1",
		"if true; then :; fi",
		"sudo() { : }", // a stand-in, so the test never runs the real sudo
		"sudo -n -u nobody git status",
		"env -u HOME git status >/dev/null 2>&1",
	}
	recs, out := runZsh(t, true, commands, len(commands))
	if strings.Contains(out, "__burnout_preexec:") || strings.Contains(out, "__burnout_precmd:") {
		t.Fatalf("hook printed an error: %s", out)
	}
	got := byArgv0(recs)
	if len(got["tool"]) != 1 {
		t.Errorf("~/bin/tool: want one record named tool: %v", keys(got))
	}
	// The quoted ~/ path, the missing ~/CANARYEEE, the if statement and the
	// function definition.
	if len(got[""]) != 4 {
		t.Errorf("unresolved commands: want 4 with an empty name, got %d: %v", len(got[""]), keys(got))
	}
	gits := got["git"]
	if len(gits) != 5 {
		t.Errorf("prefixed git commands: want 5, got %d: %v", len(gits), keys(got))
	}
	for _, r := range gits {
		if r.env[EnvSubcmd] != "status" {
			t.Errorf("prefixed git lost its subcommand: %v", burnoutEnv(r))
		}
	}
	for _, r := range recs {
		if strings.Contains(r.raw, "CANARY") {
			t.Fatalf("typed text reached the emitter: %v", burnoutEnv(r))
		}
	}
}
