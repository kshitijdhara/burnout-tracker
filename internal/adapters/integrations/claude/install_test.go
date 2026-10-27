package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const ourCmd = "'/opt/burntout/bin/burntout' hook claude"

func installOpts(path, version string) InstallOptions {
	return InstallOptions{
		SettingsPath: path,
		Command:      ourCmd,
		Version:      version,
		BackupDir:    filepath.Join(filepath.Dir(path), "backups"),
		Now:          func() time.Time { return time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC) },
	}
}

// Fixtures are in Claude Code's own format (two-space indent, trailing
// newline), so an install/uninstall round trip must reproduce them byte for
// byte.
var fixtures = map[string]string{
	"empty hooks": `{
  "model": "opus",
  "hooks": {}
}
`,
	"no hooks": `{
  "model": "opus",
  "permissions": {
    "allow": [
      "Bash(git status)"
    ]
  },
  "env": {
    "FOO": "<bar & baz>"
  }
}
`,
	"other hooks": `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "~/bin/guard.sh"
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "say done"
          }
        ]
      }
    ]
  },
  "zeta": 1,
  "alpha": [
    1.50,
    "é"
  ]
}
`,
}

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallUninstallExactRestore(t *testing.T) {
	for name, orig := range fixtures {
		t.Run(name, func(t *testing.T) {
			path := writeFixture(t, orig)
			o := installOpts(path, "2.0.50")
			if err := Install(o); err != nil {
				t.Fatal(err)
			}
			installed := readFile(t, path)

			var parsed map[string]any
			if err := json.Unmarshal([]byte(installed), &parsed); err != nil {
				t.Fatalf("installed settings are invalid JSON: %v", err)
			}
			hooks := parsed["hooks"].(map[string]any)
			for _, ev := range Events("2.0.50") {
				if !strings.Contains(mustJSON(t, hooks[ev]), ourCmd) {
					t.Errorf("%s missing our hook", ev)
				}
			}
			if strings.Contains(orig, "<bar & baz>") && !strings.Contains(installed, `"<bar & baz>"`) {
				t.Error("HTML characters were escaped or the value changed")
			}

			// Idempotent: nothing is written the second time.
			if err := Install(o); err != nil {
				t.Fatal(err)
			}
			if again := readFile(t, path); again != installed {
				t.Fatal("second install changed the file")
			}
			if backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.bak")); len(backups) != 1 {
				t.Fatalf("backups = %v", backups)
			}

			if err := Uninstall(o); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, path); got != orig {
				t.Fatalf("uninstall did not restore exactly:\n%s\nwant:\n%s", got, orig)
			}
		})
	}
}

func TestInstallKeepsTopLevelOrder(t *testing.T) {
	path := writeFixture(t, fixtures["other hooks"])
	if err := Install(installOpts(path, "")); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	hooks, zeta, alpha := strings.Index(got, `"hooks"`), strings.Index(got, `"zeta"`), strings.Index(got, `"alpha"`)
	if hooks > zeta || zeta > alpha {
		t.Fatalf("key order changed:\n%s", got)
	}
	if !strings.Contains(got, "1.50") || !strings.Contains(got, `"é"`) {
		t.Fatal("number or string literal was rewritten")
	}
}

func TestInstallEntryShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Install(installOpts(path, "")); err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]matcherGroup `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &s); err != nil {
		t.Fatal(err)
	}
	for ev, groups := range s.Hooks {
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", ev, groups)
		}
		g := groups[0]
		wantMatcher := ""
		if toolEvents[ev] {
			wantMatcher = "*"
		}
		h := g.Hooks[0]
		if g.Matcher != wantMatcher || h.Type != "command" || h.Command != ourCmd || h.Timeout != hookTimeoutSeconds {
			t.Fatalf("%s: %+v", ev, g)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("new settings mode = %v", fi.Mode().Perm())
		}
	}
}

func TestUninstallKeepsOtherEntriesInSharedGroup(t *testing.T) {
	path := writeFixture(t, `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "say done"
          },
          {
            "type": "command",
            "command": "`+strings.ReplaceAll(ourCmd, `'`, `'`)+`"
          }
        ]
      }
    ]
  }
}
`)
	if err := Uninstall(installOpts(path, "")); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Contains(got, ourCmd) || !strings.Contains(got, "say done") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestRefusesInvalidSettings(t *testing.T) {
	for _, bad := range []string{`{"model": `, `[1,2]`, `{"hooks": []}`, `{"hooks": {"Stop": {}}}`, `{} trailing`} {
		path := writeFixture(t, bad)
		err := Install(installOpts(path, ""))
		if !errors.Is(err, ErrInvalidSettings) {
			t.Fatalf("Install(%q) = %v, want ErrInvalidSettings", bad, err)
		}
		if got := readFile(t, path); got != bad {
			t.Fatalf("invalid file was modified: %q", got)
		}
		if backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.bak")); len(backups) != 0 {
			t.Fatal("backup written for a refused file")
		}
	}
}

func TestUninstallMissingFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Uninstall(installOpts(path, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("uninstall created a settings file")
	}
}

func TestEventsVersionGating(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", false},
		{"garbage", false},
		{"2.0.44", false},
		{"2.0.45", true},
		{"2.1.0 (Claude Code)", true},
		{"10.0.0", true},
		{"1.9.99", false},
	}
	for _, tc := range tests {
		ev := Events(tc.version)
		if got := slices.Contains(ev, "PermissionRequest"); got != tc.want {
			t.Errorf("Events(%q) PermissionRequest = %v, want %v", tc.version, got, tc.want)
		}
		if slices.Contains(ev, "StopFailure") {
			t.Errorf("Events(%q) includes StopFailure", tc.version)
		}
		if !slices.Contains(ev, "UserPromptSubmit") {
			t.Errorf("Events(%q) missing base events", tc.version)
		}
	}
}

func TestCommandForQuotes(t *testing.T) {
	got := CommandFor("/a/it's/burntout")
	if got != `'/a/it'\''s/burntout' hook claude || true` {
		t.Fatalf("got %s", got)
	}
	if !isOurs(got) {
		t.Fatal("CommandFor output is not recognised as ours")
	}
}

func TestIsOurs(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{`'/opt/burntout/bin/burntout' hook claude`, true},
		{`'/opt/burntout/bin/burntout' hook claude || true`, true},
		{`'/Users/me/.local/bin/burntout' hook claude || true`, true},
		{`'C:\Users\me\burntout.exe' hook claude || true`, true},
		{`/usr/local/bin/burntout hook claude`, true},
		{`'/a/it'\''s/burntout' hook claude`, true},
		{`'/opt/other/notburntout' hook claude`, false},
		{`'/opt/burntout/bin/burntout' hook codex`, false},
		{`'/opt/burntout/bin/burntout' hook claude; rm -rf ~`, false},
		{`echo hi`, false},
		{`'/unterminated hook claude`, false},
		{`'/a/burntout' '/b' hook claude`, false},
		// From before the rename: ours, so install replaces it.
		{`'/Users/me/.local/bin/burnout' hook claude || true`, true},
		{`'C:\Users\me\burnout.exe' hook claude || true`, true},
		{`'/opt/other/notburnout' hook claude`, false},
	}
	for _, tc := range tests {
		if got := isOurs(tc.cmd); got != tc.want {
			t.Errorf("isOurs(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// A binary that moved (rebuilt elsewhere, reinstalled) must replace the old
// entry instead of adding a second one, and uninstall from the new path must
// remove the old entry too. Other hooks stay untouched.
func TestInstallAfterBinaryMoved(t *testing.T) {
	path := writeFixture(t, `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "notify-send done"
          }
        ]
      }
    ]
  }
}
`)
	old := installOpts(path, "")
	old.Command = CommandFor("/old/place/burntout")
	if err := Install(old); err != nil {
		t.Fatal(err)
	}
	moved := installOpts(path, "")
	moved.Command = CommandFor("/new/place/burntout")
	if err := Install(moved); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "/old/place") {
		t.Fatalf("old entry left behind:\n%s", got)
	}
	if n := strings.Count(got, "/new/place/burntout"); n != len(Events("")) {
		t.Fatalf("%d entries for the new binary, want one per event (%d)", n, len(Events("")))
	}
	if !strings.Contains(got, "notify-send done") {
		t.Fatal("unrelated user hook removed")
	}

	// Reinstall the old path again, then uninstall with only the new options:
	// every entry of ours goes, whatever its path.
	if err := Install(old); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(moved); err != nil {
		t.Fatal(err)
	}
	got = readFile(t, path)
	if strings.Contains(got, "burntout") {
		t.Fatalf("our entries left after uninstall:\n%s", got)
	}
	if !strings.Contains(got, "notify-send done") {
		t.Fatal("unrelated user hook removed by uninstall")
	}
}

func TestSubagentStopNotRegistered(t *testing.T) {
	for _, v := range []string{"", "1.0.0", "2.0.45", "9.9.9"} {
		if slices.Contains(Events(v), "SubagentStop") {
			t.Fatalf("Events(%q) registers SubagentStop", v)
		}
	}
}

func TestDefaultSettingsPathHonoursConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/x/claude")
	got, err := DefaultSettingsPath()
	if err != nil || got != filepath.Join("/x/claude", "settings.json") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
