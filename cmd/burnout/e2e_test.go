//go:build !windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestEndToEnd builds the real binary and runs the Phase 0 loop: daemon,
// zsh hook, Claude Code hook, status. It is the privacy canary test from
// privacy & security §12: secrets planted in command arguments, prompts,
// tool input and paths must not appear in anything the app writes, hooks
// must print nothing, and the emitter must never see the command line.
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	const canary = "CANARYe2eQ7"
	tmp := t.TempDir()
	bin := buildBinary(t)

	// The unix socket path must stay short (sun_path is ~104 bytes on macOS).
	runtimeDir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	home := filepath.Join(tmp, "home")
	dataDir := filepath.Join(tmp, "data")
	configDir := filepath.Join(tmp, "config")
	projectDir := filepath.Join(home, "src", canary+"-project")
	for _, d := range []string{home, projectDir, filepath.Join(home, ".claude")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(),
		"HOME="+home, "ZDOTDIR="+home,
		"BURNOUT_DATA_DIR="+dataDir, "BURNOUT_CONFIG_DIR="+configDir, "BURNOUT_RUNTIME_DIR="+runtimeDir,
		"CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude"),
		envKeystore+"=file", // never touch the developer's real keychain
		"PATH="+os.Getenv("PATH"),
	)
	run := func(stdin string, args ...string) (stdout, stderr string, err error) {
		cmd := exec.Command(bin, args...)
		cmd.Env, cmd.Dir = env, projectDir
		cmd.Stdin = strings.NewReader(stdin)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		err = cmd.Run()
		return o.String(), e.String(), err
	}

	// Consent is required before the daemon starts (AC-3). The service's
	// daemon exits cleanly instead, so it isn't restarted every 30 s.
	if _, _, err := run("", "daemon"); err == nil {
		t.Fatal("daemon started without consent")
	}
	if _, _, err := run("", daemonServiceArgs...); err != nil {
		t.Fatalf("service daemon without consent: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dataDir, "logs", "daemon.log")); !strings.Contains(string(b), "no consent") { //nolint:gosec // test file
		t.Fatalf("service daemon didn't log why it stopped:\n%s", b)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, consentFile), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var daemonLog bytes.Buffer
	daemon := exec.Command(bin, daemonServiceArgs...) // as the background service runs it
	daemon.Env, daemon.Stdout, daemon.Stderr = env, &daemonLog, &daemonLog
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill() })
	waitFor(t, &daemonLog, func() bool {
		c, err := net.Dial("unix", filepath.Join(runtimeDir, "burnout.sock"))
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})

	// Claude Code hooks: silent, exit 0, and no canary passes through.
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		payload := fmt.Sprintf(`{"session_id":"sess-1","hook_event_name":%q,"tool_name":"Bash","cwd":%q,`+
			`"prompt":"my password is %s","tool_input":{"command":"echo %s"},"transcript_path":"/tmp/%s.jsonl"}`,
			event, projectDir, canary, canary, canary)
		out, errOut, err := run(payload, "hook", "claude")
		if err != nil || out != "" || errOut != "" {
			t.Fatalf("hook %s: err=%v stdout=%q stderr=%q", event, err, out, errOut)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Garbage input must also be silent.
	if out, errOut, err := run("not json "+canary, "hook", "claude"); err != nil || out != "" || errOut != "" {
		t.Fatalf("hook on garbage: err=%v stdout=%q stderr=%q", err, out, errOut)
	}

	if _, err := exec.LookPath("zsh"); err == nil {
		if out, _, err := run("", "install-hooks", "--claude=false"); err != nil {
			t.Fatalf("install-hooks: %v\n%s", err, out)
		}
		// A deadline turns a zsh that waits on input into a fast failure
		// instead of a hang until go test's own timeout.
		zctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		zsh := exec.CommandContext(zctx, "zsh", "-i")
		zsh.WaitDelay = time.Second
		zsh.Env, zsh.Dir = env, projectDir
		// An interactive zsh reads from /dev/tty when it has one, which would
		// take over the developer's terminal. A new session has no
		// controlling terminal, so zsh reads the script from stdin instead.
		zsh.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		zsh.Stdin = strings.NewReader(strings.Join([]string{
			"git -c user.name=" + canary + " status >/dev/null 2>&1",
			"printf '%s' " + canary + " >/dev/null",
			canary + "_not_a_command 2>/dev/null",
			"sleep 0.2",
		}, "\n") + "\n")
		var zout bytes.Buffer
		zsh.Stdout, zsh.Stderr = &zout, &zout
		if err := zsh.Run(); zctx.Err() != nil {
			t.Fatalf("zsh still running after 30s (waiting on a terminal?): %s", zout.String())
		} else if err != nil {
			t.Logf("zsh exited: %v (%s)", err, zout.String())
		}
		time.Sleep(300 * time.Millisecond) // background emitters
	} else {
		t.Log("zsh not installed; skipping the shell part")
	}

	if err := daemon.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon exit: %v\n%s", err, daemonLog.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop")
	}

	// The service's log goes to a rotating, owner-only file in the data folder.
	logPath := filepath.Join(dataDir, "logs", "daemon.log")
	if b, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(b), "daemon started") {
		t.Fatalf("daemon log %s: %v\n%s", logPath, err, b)
	}
	if fi, err := os.Stat(logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("daemon log mode: %v %v", fi, err)
	}

	status, _, err := run("", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	t.Logf("status:\n%s", status)
	if !strings.Contains(status, "Strain: no completed days recorded yet") {
		t.Fatalf("status missing the strain line:\n%s", status)
	}
	if !strings.Contains(status, "Weekly check-in due") {
		t.Fatalf("status missing the check-in reminder:\n%s", status)
	}
	if out, _, err := run("", "review"); err != nil || !strings.Contains(out, "Check-in: none yet.") {
		t.Fatalf("review: %v\n%s", err, out)
	}
	if _, errOut, err := run("", "checkin"); err == nil || !strings.Contains(errOut, "interactively") {
		t.Fatalf("checkin without a terminal: %v %s", err, errOut)
	}
	if !strings.Contains(status, "Agents: 1 sessions") {
		t.Fatalf("status missing agent session:\n%s", status)
	}
	if _, err := exec.LookPath("zsh"); err == nil && !strings.Contains(status, "Shell: ") {
		t.Fatalf("status missing shell commands:\n%s", status)
	}

	// Canary scan: every file the app wrote, plus its log output.
	scan := func(root string) {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err == nil && bytes.Contains(b, []byte(canary)) {
				t.Errorf("canary found in %s", p)
			}
			return nil
		})
	}
	scan(dataDir)
	scan(configDir)
	scan(runtimeDir)
	if strings.Contains(daemonLog.String(), canary) {
		t.Errorf("canary found in daemon log")
	}
	if strings.Contains(status, canary) {
		t.Errorf("canary found in status output")
	}
}

// buildBinary builds the real burnout binary for a test. Not in tmp:
// install-hooks refuses a binary in a folder everyone can write to.
func buildBinary(t *testing.T) string {
	t.Helper()
	binDir, err := os.MkdirTemp(".", "e2e-bin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(binDir) })
	bin, err := filepath.Abs(filepath.Join(binDir, "burnout"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func waitFor(t *testing.T, log *bytes.Buffer, ok func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the daemon; its log:\n%s", log.String())
}
