package service

import (
	"encoding/binary"
	"encoding/xml"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// These run on every OS: the Windows backend only talks to schtasks and
// powershell through the Runner.

const (
	testSID   = "S-1-5-21-1004336348-1177238915-682003330-1001"
	testExe   = `C:\Users\me\AppData\Local\Programs\burnout\burnout.exe`
	stQuery   = "schtasks /Query /TN burnout-tracker-" + testSID
	stEnd     = "schtasks /End /TN burnout-tracker-" + testSID
	stRun     = "schtasks /Run /TN burnout-tracker-" + testSID
	stDelete  = "schtasks /Delete /TN burnout-tracker-" + testSID + " /F"
	psCommand = "powershell -NoProfile -NonInteractive -Command (Get-ScheduledTask -TaskPath '\\' -TaskName 'burnout-tracker-" + testSID + "' -ErrorAction Stop).State"
)

func stCreate(path string) string {
	return "schtasks /Create /XML " + path + " /TN burnout-tracker-" + testSID + " /F"
}

func taskManager(t *testing.T, r *fakeRunner) Manager {
	m := manager(t, "windows", r)
	m.Bin, m.Env, m.SID = testExe, nil, testSID
	return m
}

// decodeTask turns RenderTask's UTF-16LE output back into a string.
func decodeTask(t *testing.T, b []byte) string {
	t.Helper()
	if len(b) < 2 || b[0] != 0xff || b[1] != 0xfe || len(b)%2 != 0 {
		t.Fatalf("not UTF-16LE with a BOM: % x", b[:min(len(b), 8)])
	}
	u := make([]uint16, 0, len(b)/2-1)
	for i := 2; i < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:]))
	}
	return string(utf16.Decode(u))
}

func TestTaskInstallUninstall(t *testing.T) {
	r := &fakeRunner{fail: map[string]bool{stEnd: true}}
	m := taskManager(t, r)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	path, _ := m.Path()
	if want := []string{stCreate(path), stEnd, stRun}; !slices.Equal(r.calls, want) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
	def, err := os.ReadFile(path) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	if s := decodeTask(t, def); !strings.Contains(s, "<Command>"+testExe+"</Command>") {
		t.Fatalf("task lacks the binary:\n%s", s)
	}
	// Task Scheduler keeps no output, so the crash log is made ready.
	if _, err := os.Stat(m.LogHint()); err != nil {
		t.Fatalf("crash log: %v", err)
	}

	r.calls = nil
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if want := []string{stQuery, stEnd, stDelete}; !slices.Equal(r.calls, want) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("task XML left behind")
	}
}

func TestTaskUninstallWhenNotInstalled(t *testing.T) {
	r := &fakeRunner{fail: map[string]bool{stQuery: true}}
	if err := taskManager(t, r).Uninstall(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.calls, []string{stQuery}) {
		t.Fatalf("calls = %q: nothing to end or delete", r.calls)
	}
}

func TestTaskUninstallKeepsATaskItCannotDelete(t *testing.T) {
	r := &fakeRunner{}
	m := taskManager(t, r)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	r.fail = map[string]bool{stDelete: true}
	r.out = map[string]string{stDelete: "ERROR: Access is denied."}
	if err := m.Uninstall(); err == nil || !strings.Contains(err.Error(), "Access is denied") {
		t.Fatalf("err = %v", err)
	}
	path, _ := m.Path()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("XML removed while the task still exists: %v", err)
	}
}

func TestTaskFailedNewInstallCleansUp(t *testing.T) {
	r := &fakeRunner{}
	m := taskManager(t, r)
	path, _ := m.Path()
	r.fail = map[string]bool{stCreate(path): true}
	r.out = map[string]string{stCreate(path): "ERROR: The task XML is malformed."}
	if err := m.Install(); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("task XML left behind after a failed install")
	}
}

func TestTaskRefusesFolderOverrides(t *testing.T) {
	r := &fakeRunner{}
	m := taskManager(t, r)
	m.Env = map[string]string{"BURNOUT_DATA_DIR": `D:\burnout`}
	if err := m.Install(); err == nil || !strings.Contains(err.Error(), "BURNOUT_DATA_DIR") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("calls = %q", r.calls)
	}
}

func TestTaskStatus(t *testing.T) {
	for _, tc := range []struct {
		name               string
		r                  *fakeRunner
		installed, running bool
		detail             string
		wantErr            bool
	}{
		{"running", &fakeRunner{out: map[string]string{psCommand: "Running\r\n"}}, true, true, "task running", false},
		{"ready", &fakeRunner{out: map[string]string{psCommand: "Ready\r\n"}}, true, false, "task ready", false},
		{"not installed", &fakeRunner{fail: map[string]bool{stQuery: true}}, false, false, "", false},
		{"no answer", &fakeRunner{fail: map[string]bool{psCommand: true}, out: map[string]string{psCommand: "Get-ScheduledTask : Access denied"}}, true, false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := taskManager(t, tc.r).Status()
			if (err != nil) != tc.wantErr || st.Installed != tc.installed || st.Running != tc.running || st.Detail != tc.detail {
				t.Fatalf("status = %+v, %v", st, err)
			}
		})
	}
}

func TestRenderTask(t *testing.T) {
	def, err := RenderTask(`C:\Program Files\R&D <tools>\burnout.exe`, testArgs, testSID)
	if err != nil {
		t.Fatal(err)
	}
	s := decodeTask(t, def)
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Error("lines aren't CRLF")
	}
	var task struct {
		Triggers struct {
			Logon struct {
				UserID string `xml:"UserId"`
			} `xml:"LogonTrigger"`
		}
		Principals struct {
			Principal struct {
				UserID    string `xml:"UserId"`
				LogonType string
				RunLevel  string
			}
		}
		Settings struct {
			MultipleInstancesPolicy                            string
			DisallowStartIfOnBatteries, StopIfGoingOnBatteries bool
			ExecutionTimeLimit                                 string
			RestartOnFailure                                   struct {
				Interval string
				Count    int
			}
		}
		Actions struct {
			Exec struct{ Command, Arguments string }
		}
	}
	// encoding/xml reads only UTF-8; the content is the same.
	if err := xml.Unmarshal([]byte(strings.Replace(s, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)), &task); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, s)
	}
	p, set := task.Principals.Principal, task.Settings
	for _, c := range []struct{ got, want string }{
		{task.Triggers.Logon.UserID, testSID}, // only this user's logon
		{p.UserID, testSID},
		{p.LogonType, "InteractiveToken"}, // only in the user's desktop session
		{p.RunLevel, "LeastPrivilege"},
		{set.MultipleInstancesPolicy, "IgnoreNew"},
		{set.ExecutionTimeLimit, "PT0S"}, // the default stops it after 72 h
		{set.RestartOnFailure.Interval, "PT1M"},
		{task.Actions.Exec.Command, `C:\Program Files\R&D <tools>\burnout.exe`},
		{task.Actions.Exec.Arguments, "daemon --service"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if set.DisallowStartIfOnBatteries || set.StopIfGoingOnBatteries {
		t.Error("the daemon must run on battery")
	}
	if set.RestartOnFailure.Count != 999 {
		t.Errorf("restart count %d", set.RestartOnFailure.Count)
	}
}

func TestRenderTaskRejectsUnsafeValues(t *testing.T) {
	for name, f := range map[string]func() error{
		"relative bin":   func() error { _, err := RenderTask(`burnout.exe`, testArgs, testSID); return err },
		"unix bin":       func() error { _, err := RenderTask(`/opt/burnout`, testArgs, testSID); return err },
		"quote in bin":   func() error { _, err := RenderTask(`C:\a"b\burnout.exe`, testArgs, testSID); return err },
		"percent in bin": func() error { _, err := RenderTask(`C:\%USERNAME%\burnout.exe`, testArgs, testSID); return err },
		"space in arg":   func() error { _, err := RenderTask(testExe, []string{"daemon", "a b"}, testSID); return err },
		"newline in bin": func() error { _, err := RenderTask("C:\\a\nb\\burnout.exe", testArgs, testSID); return err },
		"name for SID":   func() error { _, err := RenderTask(testExe, testArgs, `DOMAIN\me`); return err },
		"empty SID":      func() error { _, err := RenderTask(testExe, testArgs, ""); return err },
		"SID injection":  func() error { _, err := RenderTask(testExe, testArgs, "S-1-5-21-1</UserId>"); return err },
	} {
		if f() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := RenderTask(`\\server\share\burnout.exe`, testArgs, testSID); err != nil {
		t.Errorf("UNC path rejected: %v", err)
	}
}
