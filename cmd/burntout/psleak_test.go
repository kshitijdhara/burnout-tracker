//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/testsupport"
)

// psCanary is planted in typed commands and hook payloads. It must never
// appear in another process's command line or environment.
const psCanary = "CANARYps9Q"

// TestPSLeak is the ps-leak test from privacy & security §12: while hooked
// commands and Claude Code hooks run against the real binary and daemon,
// no process listing (`ps`, /proc/*/cmdline, and environments where the OS
// shows them) contains anything typed or sent to a hook.
//
// The typed commands are zsh builtins and subshells, so the only processes
// that could carry the canary are ones burntout's hooks start. The test
// fails unless the sampler actually saw those hook processes, so a sampler
// that misses everything can't pass it.
func TestPSLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	tmp := t.TempDir()
	bin := buildBinary(t)
	runtimeDir, err := os.MkdirTemp("", "bt") // sun_path is short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	home := filepath.Join(tmp, "home")
	configDir := filepath.Join(tmp, "config")
	for _, d := range []string{filepath.Join(home, ".claude"), configDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(configDir, consentFile), []byte(consentStamp(time.Now())), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home, "ZDOTDIR=" + home, "PATH=" + os.Getenv("PATH"), "TERM=dumb",
		"BURNTOUT_DATA_DIR=" + filepath.Join(tmp, "data"), "BURNTOUT_CONFIG_DIR=" + configDir,
		"BURNTOUT_RUNTIME_DIR=" + runtimeDir, "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"),
		envKeystore + "=file",
	}
	if out, err := runWith(env, bin, "install-hooks", "--claude=false", "--bash", "--fish"); err != nil {
		t.Fatalf("install-hooks: %v\n%s", err, out)
	}

	var daemonLog bytes.Buffer
	daemon := exec.Command(bin, "daemon")
	daemon.Env, daemon.Stdout, daemon.Stderr = env, &daemonLog, &daemonLog
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Signal(syscall.SIGTERM); _ = daemon.Wait() })
	waitFor(t, &daemonLog, func() bool {
		c, err := net.Dial("unix", filepath.Join(runtimeDir, "burntout.sock"))
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})

	s := startSampler(t, bin)
	defer s.stop()

	// Claude Code hook: the payload carries the canary in every field
	// BurntOut ignores. stdin stays open until the sampler has seen the
	// hook process, so it is caught while it runs.
	for _, event := range []string{"UserPromptSubmit", "PreToolUse"} {
		hook := exec.Command(bin, "hook", "claude")
		hook.Env, hook.Dir = env, home
		stdin, err := hook.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := hook.Start(); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(stdin, `{"session_id":"s-1","hook_event_name":%q,"tool_name":"Bash","cwd":%q,`+
			`"prompt":"secret %s","tool_input":{"command":"echo %s"},"transcript_path":"/x/%s.jsonl"`,
			event, home, psCanary, psCanary, psCanary)
		s.waitSeen(t, "hook claude", 5*time.Second)
		_, _ = io.WriteString(stdin, "}")
		_ = stdin.Close()
		if err := hook.Wait(); err != nil {
			t.Fatalf("hook: %v", err)
		}
	}

	// zsh: each command line holds the canary; each one makes precmd start
	// a short-lived emitter, so enough of them are run for the sampler to
	// catch at least one.
	var lines []string
	for i := range 150 {
		switch i % 3 {
		case 0:
			lines = append(lines, "print -r -- "+psCanary+" >/dev/null")
		case 1:
			lines = append(lines, "( : "+psCanary+" ) >/dev/null")
		default:
			lines = append(lines, "x="+psCanary+"; typeset y=$x")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	zsh := exec.CommandContext(ctx, "zsh", "-i")
	zsh.WaitDelay = time.Second
	zsh.Env, zsh.Dir = env, home
	zsh.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // no controlling terminal: read stdin
	zsh.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\nsleep 0.5\n")
	if out, err := zsh.CombinedOutput(); ctx.Err() != nil {
		t.Fatalf("zsh still running after 60s: %s", out)
	} else if err != nil {
		t.Logf("zsh exited: %v (%s)", err, out)
	}

	// bash and fish, through what install-hooks wrote to ~/.bashrc and
	// fish's conf.d, typed into a terminal as their hooks need.
	for _, sh := range []struct {
		argv  []string
		diag  []string // typed first; their output is shown if no emitter is seen
		lines []string
	}{
		{[]string{"bash", "-i"}, []string{
			`echo "PC=[${PROMPT_COMMAND[*]}] BP=[${bash_preexec_imported:-}] HOOK=[$(type -t __burntout_preexec)]"`,
			`trap -p DEBUG`, `grep -n -i -e PROMPT_COMMAND -e trap -e preexec /etc/bash.bashrc 2>/dev/null | head -20`,
		}, []string{": " + psCanary, "x=" + psCanary, "( : " + psCanary + " )"}},
		{[]string{"fish", "-i"}, nil, []string{"set x " + psCanary, "echo " + psCanary + " >/dev/null", "true " + psCanary}},
	} {
		python, err := exec.LookPath("python3")
		if err != nil {
			t.Log("python3 not installed: bash and fish not checked")
			break
		}
		if _, err := exec.LookPath(sh.argv[0]); err != nil {
			t.Logf("%s not installed: not checked", sh.argv[0])
			continue
		}
		before := s.count("emit shell")
		// As many commands as zsh gets: each emitter lives a few
		// milliseconds, and a slow CI runner catches only some of them.
		typed := append([]string(nil), sh.diag...)
		for i := range 150 {
			typed = append(typed, sh.lines[i%len(sh.lines)])
		}
		typed = append(typed, "exit")
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		cmd := exec.CommandContext(ctx, python, append([]string{"-c", testsupport.PTYType}, sh.argv...)...) //nolint:gosec // test tools
		// Each emitter stays up 200 ms, so the sampler sees some even on a
		// loaded runner, where a few milliseconds slip between its scans.
		cmd.Env, cmd.Dir = append(env, "BT_TYPE_DELAY=0.05", envEmitHold+"=200ms"), home
		cmd.Stdin = strings.NewReader(strings.Join(typed, "\n") + "\n")
		out, _ := cmd.CombinedOutput()
		cancel()
		if n := s.count("emit shell") - before; n == 0 && runtime.GOOS == "linux" {
			t.Fatalf("the sampler never saw a %s emitter, so it checked nothing:\n%s", sh.argv[0], out)
		} else {
			t.Logf("%s: saw its emitter %d times", sh.argv[0], n)
		}
	}

	leaks, seen, scans := s.stop()
	t.Logf("%d process-table scans; saw hook claude %d times, emit shell %d times", scans, seen["hook claude"], seen["emit shell"])
	for _, l := range leaks {
		t.Errorf("canary in a process listing: %s", l)
	}
	if seen["emit shell"] == 0 {
		if runtime.GOOS == "linux" {
			t.Fatal("the sampler never saw a shell emitter, so it checked nothing")
		}
		// ps on macOS is slow next to the emitter's few milliseconds; the
		// hook processes above were still checked.
		t.Log("no shell emitter caught by ps this run")
	}
}

func runWith(env []string, bin string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

// sampler scans the process table in a loop and records any process whose
// command line or (readable) environment holds psCanary.
type sampler struct {
	bin  string
	done chan struct{}
	wg   sync.WaitGroup

	mu    sync.Mutex
	leaks map[string]bool
	seen  map[string]int
	scans int
}

func startSampler(t *testing.T, bin string) *sampler {
	s := &sampler{bin: bin, done: make(chan struct{}), leaks: map[string]bool{}, seen: map[string]int{}}
	if _, err := listProcesses(); err != nil {
		t.Skipf("can't list processes: %v", err)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-s.done:
				return
			default:
			}
			procs, err := listProcesses()
			if err != nil {
				continue
			}
			s.record(procs)
		}
	}()
	return s
}

func (s *sampler) record(procs []process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans++
	for _, p := range procs {
		if strings.Contains(p.cmdline, psCanary) || strings.Contains(p.environ, psCanary) {
			s.leaks[fmt.Sprintf("pid %d: %q", p.pid, p.cmdline)] = true
		}
		for _, what := range []string{"hook claude", "emit shell"} {
			// On macOS the environment follows the command after a space.
			if c := s.bin + " " + what; p.cmdline == c || strings.HasPrefix(p.cmdline, c+" ") {
				s.seen[what]++
			}
		}
	}
}

func (s *sampler) count(what string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[what]
}

func (s *sampler) waitSeen(t *testing.T, what string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := s.seen[what]
		s.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the sampler never saw %q", what)
}

// stop ends sampling (once) and returns the leaks, what was seen, and how
// many scans ran.
func (s *sampler) stop() (leaks []string, seen map[string]int, scans int) {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for l := range s.leaks {
		leaks = append(leaks, l)
	}
	return leaks, s.seen, s.scans
}

type process struct {
	pid              int
	cmdline, environ string
}

// listProcesses reads every process's command line, and its environment
// where readable: /proc on Linux, ps -E on macOS (which shows the
// environment of the user's own processes).
func listProcesses() ([]process, error) {
	if runtime.GOOS == "linux" {
		return listProc()
	}
	out, err := exec.Command("ps", "-axwwE", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var procs []process
	for _, line := range strings.Split(string(out), "\n") {
		var p process
		line = strings.TrimSpace(line)
		pid, rest, ok := strings.Cut(line, " ")
		if _, err := fmt.Sscan(pid, &p.pid); !ok || err != nil {
			continue
		}
		// -E appends the environment to the command; both are checked.
		p.cmdline = strings.TrimSpace(rest)
		procs = append(procs, p)
	}
	return procs, nil
}

func listProc() ([]process, error) {
	dirs, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, errors.New("no processes in /proc")
	}
	procs := make([]process, 0, len(dirs))
	for _, d := range dirs {
		var p process
		if _, err := fmt.Sscan(filepath.Base(d), &p.pid); err != nil {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join(d, "cmdline")) //nolint:gosec // reading the process table is the test
		if err != nil {
			continue // exited
		}
		p.cmdline = strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " "))
		if env, err := os.ReadFile(filepath.Join(d, "environ")); err == nil { //nolint:gosec // as above
			p.environ = string(env)
		}
		procs = append(procs, p)
	}
	return procs, nil
}
