package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/fileedit"
)

// ErrInvalidSettings means the existing settings file isn't a JSON object.
// We never overwrite a file we can't parse.
var ErrInvalidSettings = errors.New("claude: settings file is not a valid JSON object")

// hookTimeoutSeconds is generous on purpose: the hook itself gives up after
// 50 ms, so this only matters if something is badly wrong.
const hookTimeoutSeconds = 5

// baseEvents are hook events every supported Claude Code version knows.
// StopFailure is deliberately absent in Phase 0: we haven't verified which
// version introduced it, and an unknown event name could make Claude Code
// reject the settings file.
// SubagentStop is not registered: the tracker has no use for it, and every
// registered event costs a process spawn.
var baseEvents = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"Notification", "Stop",
}

// permissionRequestSince is the first Claude Code version with the
// PermissionRequest hook event.
var permissionRequestSince = [3]int{2, 0, 45}

// toolEvents take a matcher; "*" matches every tool.
var toolEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true}

// InstallOptions says where and what to install.
type InstallOptions struct {
	SettingsPath string // user-level settings, see DefaultSettingsPath
	Command      string // the hook command line, see CommandFor; unused by Uninstall
	Version      string // Claude Code version from DetectVersion; "" if unknown
	BackupDir    string // where the pre-edit copy goes (the app's private data dir)
	Now          func() time.Time
}

// DefaultSettingsPath returns the user-level settings file:
// $CLAUDE_CONFIG_DIR/settings.json, or ~/.claude/settings.json. Managed and
// project-level settings are never touched.
func DefaultSettingsPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// CommandFor returns the hook command for the given burnout binary. The
// trailing `|| true` guarantees exit 0 even if the Go runtime itself crashes
// (exit 2), which Claude Code would treat as blocking the user's action.
func CommandFor(binPath string) string {
	return fileedit.ShellQuote(binPath) + " " + hookSuffix + " " + orTrue
}

const (
	hookSuffix = "hook claude"
	orTrue     = "|| true"
)

// isOurs reports whether a hook command is one of ours, wherever the
// binary lived when it was installed: `<burnout binary> hook claude`, with
// an optional trailing `|| true`. Matching on the binary's base name rather
// than its full path keeps install idempotent after the binary moves.
func isOurs(command string) bool {
	c := strings.TrimSpace(command)
	c = strings.TrimSpace(strings.TrimSuffix(c, orTrue))
	if !strings.HasSuffix(c, " "+hookSuffix) {
		return false
	}
	bin, ok := firstShellWord(strings.TrimSuffix(c, " "+hookSuffix))
	if !ok {
		return false
	}
	base := strings.ToLower(bin[strings.LastIndexAny(bin, `/\`)+1:])
	return base == "burnout" || base == "burnout.exe"
}

// firstShellWord unquotes a word written by fileedit.ShellQuote (single
// quotes, with ' as '\”) or a bare word, and requires it to be the whole
// string.
func firstShellWord(s string) (string, bool) {
	if !strings.HasPrefix(s, "'") {
		return s, s != "" && !strings.ContainsAny(s, " '")
	}
	var b strings.Builder
	for rest := s[1:]; ; {
		i := strings.IndexByte(rest, '\'')
		if i < 0 {
			return "", false
		}
		b.WriteString(rest[:i])
		rest = rest[i+1:]
		switch {
		case rest == "":
			return b.String(), true
		case strings.HasPrefix(rest, `\''`):
			b.WriteByte('\'')
			rest = rest[3:]
		default:
			return "", false
		}
	}
}

var reSemver = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// DetectVersion runs `claude --version` and returns its semver, or "" if
// Claude Code isn't installed or doesn't answer within 2 s.
func DetectVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--version").Output()
	if err != nil {
		return ""
	}
	return reSemver.FindString(string(out))
}

// Events returns the hook events to register for a Claude Code version.
func Events(version string) []string {
	ev := append([]string(nil), baseEvents...)
	if atLeast(version, permissionRequestSince) {
		ev = append(ev, "PermissionRequest")
	}
	return ev
}

func atLeast(version string, want [3]int) bool {
	m := reSemver.FindStringSubmatch(version)
	if m == nil {
		return false
	}
	for i := range 3 {
		n, _ := strconv.Atoi(m[i+1])
		if n != want[i] {
			return n > want[i]
		}
	}
	return true
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type matcherGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

// Install makes each event in Events(o.Version) carry exactly one of our
// hooks, with the current command, leaving every other key and hook
// untouched. Entries left by an older install (another binary path) are
// replaced, so running it again, even after the binary moved, never
// duplicates hooks.
func Install(o InstallOptions) error {
	if o.SettingsPath == "" || o.Command == "" || o.BackupDir == "" || o.Now == nil {
		return errors.New("claude: incomplete install options")
	}
	return editSettings(o.SettingsPath, o.BackupDir, o.Now(), true, func(hooks *object) (bool, error) {
		changed := false
		for _, event := range Events(o.Version) {
			var groups []json.RawMessage
			if raw, ok := hooks.get(event); ok {
				var err error
				if groups, err = decodeArray(raw); err != nil {
					return false, fmt.Errorf("%w: hooks.%s is not an array", ErrInvalidSettings, event)
				}
			}
			if ours := ourCommands(groups); len(ours) == 1 && ours[0] == o.Command {
				continue
			}
			groups, _, err := removeOurs(groups)
			if err != nil {
				return false, err
			}
			g := matcherGroup{Hooks: []hookEntry{{Type: "command", Command: o.Command, Timeout: hookTimeoutSeconds}}}
			if toolEvents[event] {
				g.Matcher = "*"
			}
			raw, err := marshal(g)
			if err != nil {
				return false, err
			}
			arr, err := compactArray(append(groups, raw))
			if err != nil {
				return false, err
			}
			hooks.set(event, arr)
			changed = true
		}
		return changed, nil
	})
}

// Uninstall removes every hook entry of ours (whatever binary path it was
// installed with), then any matcher groups, event arrays and hooks object
// that removal left empty. Other hooks are untouched.
func Uninstall(o InstallOptions) error {
	if o.SettingsPath == "" || o.BackupDir == "" || o.Now == nil {
		return errors.New("claude: incomplete install options")
	}
	if _, err := os.Stat(o.SettingsPath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return editSettings(o.SettingsPath, o.BackupDir, o.Now(), false, func(hooks *object) (bool, error) {
		changed := false
		for _, m := range append([]member(nil), hooks.members...) {
			groups, err := decodeArray(m.val)
			if err != nil {
				continue // not ours to fix
			}
			kept, removed, err := removeOurs(groups)
			if err != nil || !removed {
				continue
			}
			changed = true
			if len(kept) == 0 {
				hooks.del(m.key)
				continue
			}
			arr, err := compactArray(kept)
			if err != nil {
				return false, err
			}
			hooks.set(m.key, arr)
		}
		return changed, nil
	})
}

// emptyHooksMarker, in the backup directory, records that install found an
// empty "hooks" object in the settings file it names, so uninstall can put
// it back. Any other hooks object install finds is non-empty or absent, and
// uninstall restores those without help.
const emptyHooksMarker = "claude-settings.empty-hooks"

// editSettings loads the settings file, lets edit change its "hooks"
// object, and writes the result back (with a backup) only if edit reports a
// change. An empty hooks object left behind by an uninstall is removed,
// unless install found it there (see emptyHooksMarker).
func editSettings(path, backupDir string, now time.Time, install bool, edit func(hooks *object) (bool, error)) error {
	target, err := fileedit.ResolveTarget(path)
	if err != nil {
		return fmt.Errorf("claude: resolve %s: %w", path, err)
	}
	old, err := os.ReadFile(target) //nolint:gosec // the user's own Claude Code settings file
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("claude: read %s: %w", target, err)
	}
	mode := fs.FileMode(0o600)
	if existed {
		if fi, err := os.Stat(target); err == nil {
			mode = fi.Mode().Perm()
		}
	}

	src := old
	if !existed || len(bytes.TrimSpace(old)) == 0 {
		src = []byte("{}")
	}
	top, err := decodeObject(src)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}
	hooks := &object{}
	raw, hadHooks := top.get("hooks")
	if hadHooks {
		if hooks, err = decodeObject(raw); err != nil {
			return fmt.Errorf("%w: hooks is not an object", ErrInvalidSettings)
		}
	}
	foundEmpty := hadHooks && hooks.len() == 0
	marker := filepath.Join(backupDir, emptyHooksMarker)

	changed, err := edit(hooks)
	if err != nil || !changed {
		return err
	}
	// An edit only empties the hooks object by removing our own entries, so
	// the file returns to its pre-install shape: no key, or the empty object
	// install found.
	keepEmpty := !install && markerNames(marker, target)
	switch {
	case hooks.len() == 0 && keepEmpty:
		top.set("hooks", json.RawMessage("{}"))
	case hooks.len() == 0:
		top.del("hooks")
	default:
		raw, err := hooks.compact()
		if err != nil {
			return err
		}
		top.set("hooks", raw)
	}

	compact, err := top.compact()
	if err != nil {
		return err
	}
	out, err := pretty(compact)
	if err != nil {
		return err
	}
	if existed {
		if _, err := fileedit.Backup(backupDir, target, old, now); err != nil {
			return err
		}
	}
	if err := fileedit.WriteAtomic(target, out, mode); err != nil {
		return fmt.Errorf("claude: write %s: %w", target, err)
	}
	switch {
	case install && foundEmpty:
		if err := fileedit.WriteAtomic(marker, []byte(target), 0o600); err != nil {
			return fmt.Errorf("claude: record empty hooks: %w", err)
		}
	case keepEmpty && hooks.len() == 0:
		_ = os.Remove(marker) // restored; a stale marker only keeps an empty object
	}
	return nil
}

// markerNames reports whether the marker file exists and names target.
func markerNames(marker, target string) bool {
	b, err := os.ReadFile(marker) //nolint:gosec // our own file in the app's backup directory
	return err == nil && string(b) == target
}

// ourCommands lists the commands of our hook entries in groups.
func ourCommands(groups []json.RawMessage) []string {
	var out []string
	for _, raw := range groups {
		var g matcherGroup
		if json.Unmarshal(raw, &g) != nil {
			continue
		}
		for _, h := range g.Hooks {
			if isOurs(h.Command) {
				out = append(out, h.Command)
			}
		}
	}
	return out
}

// removeOurs drops our entries from each matcher group, and drops groups
// that end up empty because of it. Groups we can't parse are kept as-is.
func removeOurs(groups []json.RawMessage) (kept []json.RawMessage, removed bool, err error) {
	for _, raw := range groups {
		g, err := decodeObject(raw)
		if err != nil {
			kept = append(kept, raw)
			continue
		}
		hooksRaw, ok := g.get("hooks")
		if !ok {
			kept = append(kept, raw)
			continue
		}
		entries, err := decodeArray(hooksRaw)
		if err != nil {
			kept = append(kept, raw)
			continue
		}
		var rest []json.RawMessage
		for _, e := range entries {
			var h hookEntry
			if json.Unmarshal(e, &h) == nil && isOurs(h.Command) {
				removed = true
				continue
			}
			rest = append(rest, e)
		}
		switch {
		case len(rest) == len(entries):
			kept = append(kept, raw)
		case len(rest) == 0:
			// The group only held our hook: drop it.
		default:
			arr, err := compactArray(rest)
			if err != nil {
				return nil, false, err
			}
			g.set("hooks", arr)
			c, err := g.compact()
			if err != nil {
				return nil, false, err
			}
			kept = append(kept, c)
		}
	}
	return kept, removed, nil
}
