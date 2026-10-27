//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
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
	// Consent to an older data summary stops it the same way.
	stale := "consented " + time.Now().Format(time.RFC3339) + " version v0.1.0\n"
	if err := os.WriteFile(filepath.Join(configDir, consentFile), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run("", "daemon"); err == nil {
		t.Fatal("daemon started on stale consent")
	}
	if _, _, err := run("", daemonServiceArgs...); err != nil {
		t.Fatalf("service daemon on stale consent: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dataDir, "logs", "daemon.log")); !strings.Contains(string(b), "burnout setup` again") { //nolint:gosec // test file
		t.Fatalf("service daemon didn't log that consent is out of date:\n%s", b)
	}
	if err := os.WriteFile(filepath.Join(configDir, consentFile), []byte(consentStamp(time.Now())), 0o600); err != nil {
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

	// While paused, hook events are discarded: this session never appears.
	if out, _, err := run("", "pause", "10m"); err != nil || !strings.Contains(out, "Paused until") {
		t.Fatalf("pause: %v %s", err, out)
	}
	time.Sleep(1500 * time.Millisecond) // the daemon checks every second
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		payload := fmt.Sprintf(`{"session_id":"sess-paused","hook_event_name":%q,"cwd":%q}`, event, projectDir)
		if _, _, err := run(payload, "hook", "claude"); err != nil {
			t.Fatal(err)
		}
	}
	if out, _, _ := run("", "status"); !strings.Contains(out, "Paused until") {
		t.Fatalf("status doesn't show the pause:\n%s", out)
	}
	if out, _, err := run("", "resume"); err != nil || !strings.Contains(out, "Resumed") {
		t.Fatalf("resume: %v %s", err, out)
	}
	time.Sleep(1500 * time.Millisecond)

	// Private hours from the config file apply to the running daemon.
	serviceLog := func() string {
		b, _ := os.ReadFile(filepath.Join(dataDir, "logs", "daemon.log")) //nolint:gosec // test file
		return string(b)
	}
	if out, _, err := run("", "config", "set", "privacy.private_hours", "daily"); err != nil || !strings.Contains(out, `privacy.private_hours = ["daily"]`) {
		t.Fatalf("config set: %v %s", err, out)
	}
	waitFor(t, &daemonLog, func() bool { return strings.Contains(serviceLog(), `for="private hours"`) })
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		payload := fmt.Sprintf(`{"session_id":"sess-private","hook_event_name":%q,"cwd":%q}`, event, projectDir)
		if _, _, err := run(payload, "hook", "claude"); err != nil {
			t.Fatal(err)
		}
	}
	if out, _, _ := run("", "status"); !strings.Contains(out, "Private hours (daily)") {
		t.Fatalf("status doesn't show private hours:\n%s", out)
	}
	// A hand edit that breaks the file is reported, and the daemon keeps
	// its settings rather than dropping the user's private hours.
	cfgFile := filepath.Join(configDir, "config.toml")
	good, err := os.ReadFile(cfgFile) //nolint:gosec // test file
	if err != nil || !strings.Contains(string(good), `private_hours = ["daily"]`) {
		t.Fatalf("config file: %v\n%s", err, good)
	}
	if err := os.WriteFile(cfgFile, append(slices.Clone(good), "titles = true\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, errOut, err := run("", "config", "validate"); err == nil || !strings.Contains(errOut, "unknown keys: ") || !strings.Contains(errOut, ".titles") {
		t.Fatalf("validate accepted a bad file: %s %s", out, errOut)
	}
	waitFor(t, &daemonLog, func() bool { return strings.Contains(serviceLog(), "keeping the current settings") })
	if _, _, err := run("", "config", "set", "privacy.aggregate_only", "true"); err == nil {
		t.Fatal("config set rewrote a file it couldn't read")
	}
	if err := os.WriteFile(cfgFile, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, err := run("", "config", "unset", "privacy.private_hours"); err != nil || !strings.Contains(out, "privacy.private_hours = []") {
		t.Fatalf("config unset: %v %s", err, out)
	}
	waitFor(t, &daemonLog, func() bool { return strings.Contains(serviceLog(), `after="private hours"`) })

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

	// The dashboard serves the same data over its one-time link, and none
	// of the canaries reach it.
	checkDashboard(t, bin, env, canary)

	// Annotations, against the real database.
	if out, _, err := run("", "annotate", "vacation", "2026-11-02", "2026-11-06", "--note", "away"); err != nil || !strings.Contains(out, "left out of your baseline") {
		t.Fatalf("annotate: %v %s", err, out)
	}
	if out, _, err := run("", "annotate", "list"); err != nil || !strings.Contains(out, "vacation, 2026-11-02 to 2026-11-06 (5 days): away") {
		t.Fatalf("annotate list: %v %s", err, out)
	}
	if out, _, err := run("", "privacy"); err != nil || !strings.Contains(out, "Collection:") || !strings.Contains(out, "burnout forget") {
		t.Fatalf("privacy: %v %s", err, out)
	}

	// Export, as JSON and as CSV; both are canary-scanned below. Forget
	// can't delete them, so it points at them.
	exportDir := filepath.Join(tmp, "exports")
	if err := os.MkdirAll(exportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	jsonExport := filepath.Join(exportDir, "burnout.json")
	if out, errOut, err := run("", "export", "--out", jsonExport); err != nil || !strings.Contains(errOut, "plaintext") || !strings.Contains(out, "Exported") {
		t.Fatalf("export: %v %s %s", err, out, errOut)
	}
	var exported struct {
		V         int               `json:"v"`
		Intervals []json.RawMessage `json:"intervals"`
	}
	if b, err := os.ReadFile(jsonExport); err != nil || json.Unmarshal(b, &exported) != nil || exported.V != 1 || len(exported.Intervals) == 0 { //nolint:gosec // test file
		t.Fatalf("JSON export: %v\n%s", err, b)
	}
	if _, _, err := run("", "export", "--out", jsonExport); err == nil {
		t.Fatal("export overwrote an existing file")
	}
	if out, errOut, err := run("", "export", "--format", "csv", "--out", filepath.Join(exportDir, "csv"), "--since", "2026-01-01"); err != nil {
		t.Fatalf("CSV export: %v %s %s", err, out, errOut)
	}
	if b, err := os.ReadFile(filepath.Join(exportDir, "csv", "intervals.csv")); err != nil || !strings.HasPrefix(string(b), "start,end,day,kind") { //nolint:gosec // test file
		t.Fatalf("intervals.csv: %v\n%s", err, b)
	}

	// A diagnostics bundle needs a "yes" typed in a terminal, so it isn't
	// written here; the same bundle, built in this process from the same
	// folders, carries none of the canaries the daemon was fed.
	bundlePath := filepath.Join(exportDir, "bundle.zip")
	if _, errOut, err := run("yes\n", "doctor", "--bundle", bundlePath); err == nil || !strings.Contains(errOut, "run it in a terminal") {
		t.Fatalf("doctor --bundle without a terminal: %v %s", err, errOut)
	}
	if _, err := os.Stat(bundlePath); !os.IsNotExist(err) {
		t.Fatalf("doctor --bundle wrote a file without asking: %v", err)
	}
	report, _, _ := run("", "doctor")
	for k, v := range map[string]string{"HOME": home, paths.EnvDataDir: dataDir, paths.EnvConfigDir: configDir, envKeystore: "file"} {
		t.Setenv(k, v)
	}
	files, err := collectBundle(t.Context(), []byte(report))
	if err == nil {
		err = writeBundle(bundlePath, files)
	}
	if err != nil {
		t.Fatal(err)
	}
	bundle := readBundle(t, bundlePath)
	if !strings.Contains(bundle[paths.DaemonLogFile], "level=") {
		t.Fatalf("bundle has no daemon log: %v", bundle)
	}
	for name, data := range bundle {
		if strings.Contains(data, canary) || strings.Contains(data, home) {
			t.Errorf("canary or home folder in bundle's %s", name)
		}
	}
	if out, _, err := run("", "config", "set", "profile.core_hours", "7:30-16:30"); err != nil || !strings.Contains(out, "07:30-16:30") {
		t.Fatalf("set core hours: %v %s", err, out)
	}
	if out, _, err := run("", "config", "set", "profile.role", "manager"); err == nil {
		t.Fatalf("an unknown role was accepted: %s", out)
	}
	if _, _, err := run("", "config", "set", "profile.role", "researcher"); err != nil {
		t.Fatal(err)
	}
	if out, errOut, err := run("", "status"); err != nil || errOut != "" {
		t.Fatalf("status with a profile: %v %s", err, errOut)
	} else if !strings.Contains(out, "Strain") {
		t.Fatalf("status:\n%s", out)
	}
	if out, _, _ := run("", "privacy"); !strings.Contains(out, "2 still on disk") || !strings.Contains(out, "researcher, core hours 07:30-16:30") {
		t.Fatalf("privacy doesn't list the exports or the profile:\n%s", out)
	}

	// A calendar from a local .ics file (no network): only meeting times are
	// kept; titles, notes, places and attendees (canaries here) never are.
	icsFile := filepath.Join(tmp, "work.ics")
	ev := func(uid string, start time.Time, extra string) string {
		return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTART:" + start.UTC().Format("20060102T150405Z") +
			"\r\nDURATION:PT30M\r\nSUMMARY:Review " + canary + "\r\nDESCRIPTION:dial-in " + canary +
			"\r\nLOCATION:Room " + canary + "\r\nATTENDEE;CN=" + canary + ":mailto:x@example.com\r\n" + extra + "END:VEVENT\r\n"
	}
	base := time.Now().Add(-3 * time.Hour).Truncate(time.Minute)
	icsBody := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + ev("a", base, "") + ev("b", base.Add(30*time.Minute), "") +
		ev("daily", base.AddDate(0, 0, -5), "RRULE:FREQ=DAILY;COUNT=3\r\n") + "END:VCALENDAR\r\n"
	if err := os.WriteFile(icsFile, []byte(icsBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, err := run("", "calendar", "status"); err != nil || !strings.Contains(out, "No calendar") {
		t.Fatalf("calendar status before: %v %s", err, out)
	}
	if out, errOut, err := run("", "calendar", "add", icsFile); err != nil || !strings.Contains(out, "Added work.ics: 5 meetings") || !strings.Contains(out, "Stored 5 meetings") {
		t.Fatalf("calendar add: %v %s %s", err, out, errOut)
	}
	if out, _, err := run("", "calendar", "status"); err != nil || !strings.Contains(out, "from work.ics, last synced") {
		t.Fatalf("calendar status: %v %s", err, out)
	}
	if out, _, _ := run("", "privacy"); !strings.Contains(out, "Calendar:         from work.ics") {
		t.Fatalf("privacy doesn't show the calendar:\n%s", out)
	}
	if out, _, _ := run("", "calendar", "add", "http://calendar.example.com/feed.ics"); strings.Contains(out, "Added") {
		t.Fatal("an http:// feed was accepted")
	}

	// A daemon doesn't start on an invalid config file.
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[retention]\nminutes_days = 999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := run("", "daemon"); err == nil || !strings.Contains(errOut, "retention can only be lowered") {
		t.Fatalf("daemon started on an invalid config: %v %s", err, errOut)
	}
	if out, _, _ := run("", "doctor"); !strings.Contains(out, "config file is valid") || !strings.Contains(out, "won't start") {
		t.Fatalf("doctor doesn't report the invalid config:\n%s", out)
	}
	if err := os.Remove(filepath.Join(configDir, "config.toml")); err != nil {
		t.Fatal(err)
	}

	// Forget the last hour through a running daemon: everything above goes.
	var log2 bytes.Buffer
	daemon2 := exec.Command(bin, "daemon")
	daemon2.Env, daemon2.Stdout, daemon2.Stderr = env, &log2, &log2
	if err := daemon2.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon2.Process.Kill() })
	waitFor(t, &log2, func() bool {
		c, err := net.Dial("unix", filepath.Join(runtimeDir, "burnout.sock"))
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})
	if _, _, err := run("", "forget", "--last", "1h"); err == nil {
		t.Fatal("forget without a terminal or --yes deleted data")
	}
	if out, errOut, err := run("", "forget", "--last", "1h", "--yes"); err != nil || !strings.Contains(out, "Deleted everything recorded in the last 1h") {
		t.Fatalf("forget: %v %s %s\n%s", err, out, errOut, log2.String())
	}
	if out, _, _ := run("", "forget", "--all", "--yes"); !strings.Contains(out, "burnout.json") {
		t.Fatalf("forget doesn't point at the exports:\n%s", out)
	}
	if err := daemon2.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := daemon2.Wait(); err != nil {
		t.Fatalf("daemon after forget: %v\n%s", err, log2.String())
	}
	if out, _, _ := run("", "status"); strings.Contains(out, "Agents:") || strings.Contains(out, "Shell:") {
		t.Fatalf("data left after forgetting:\n%s", out)
	}
	if out, _, _ := run("", "annotate", "list"); !strings.Contains(out, "vacation") {
		t.Fatalf("forget removed the annotation:\n%s", out)
	}
	// The calendar stays added, but forget's meetings don't come back.
	if out, errOut, err := run("", "calendar", "sync"); err != nil || !strings.Contains(out, "0 meetings stored") {
		t.Fatalf("calendar sync after forget: %v %s %s", err, out, errOut)
	}
	if out, _, err := run("", "calendar", "remove", "--forget"); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("calendar remove: %v %s", err, out)
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
	scan(exportDir)
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
	if out, err := exec.Command("go", "build", "-tags", "e2e", "-o", bin, ".").CombinedOutput(); err != nil {
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

// checkDashboard runs `burnout dashboard --no-open`, redeems its one-time
// link and reads every API view.
func checkDashboard(t *testing.T, bin string, env []string, canary string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "dashboard", "--no-open")
	cmd.Env = env
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	var link string
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); strings.HasPrefix(l, "http://127.0.0.1:") {
			link = l
			break
		}
	}
	if link == "" {
		t.Fatal("dashboard printed no link")
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := client.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "<title>burnout") {
		t.Fatalf("dashboard page: %d\n%s", resp.StatusCode, page)
	}
	base := strings.SplitN(link, "/launch", 2)[0]
	for _, path := range []string{"/api/overview", "/api/day", "/api/week", "/api/privacy"} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || !json.Valid(body) {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
		if strings.Contains(string(body), canary) {
			t.Errorf("canary in %s", path)
		}
		if path == "/api/day" && !strings.Contains(string(body), `"agent_sessions":1`) {
			t.Errorf("today's view lacks the agent session: %s", body)
		}
	}
	if resp, err := http.Get(link); err == nil { // the link works once
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("reused link: %d", resp.StatusCode)
		}
	}
}
