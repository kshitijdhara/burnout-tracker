package shell

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/fileedit"
)

const (
	blockBegin = "# >>> burnout >>>\n"
	blockEnd   = "# <<< burnout <<<\n"
	// preexecName is the bash-preexec copy written next to the bash hook.
	preexecName = "bash-preexec.sh"
)

// Kind is a supported shell.
type Kind string

// Supported shells.
const (
	Zsh  Kind = "zsh"
	Bash Kind = "bash"
	Fish Kind = "fish"
)

// InstallOptions says where the hook goes.
type InstallOptions struct {
	Shell Kind // zsh when empty
	// RCPath is the file that sources the hook, e.g. ~/.zshrc or ~/.bashrc;
	// it may be a symlink. Empty for fish, which loads its conf.d by itself.
	RCPath    string
	ScriptDir string // where the rendered script goes: <config dir>/hooks, or fish's conf.d
	BinPath   string // absolute path of the burnout binary
	BackupDir string // where the pre-edit copy of the rc file goes (the app's private data dir)
	Now       func() time.Time
}

func (o InstallOptions) kind() Kind {
	if o.Shell == "" {
		return Zsh
	}
	return o.Shell
}

// ScriptPath is where Install writes the rendered hook script.
func (o InstallOptions) ScriptPath() string {
	return filepath.Join(o.ScriptDir, "burnout."+string(o.kind()))
}

// scripts returns every file Install writes, with its content.
func (o InstallOptions) scripts() (map[string]string, error) {
	switch o.kind() {
	case Zsh:
		return map[string]string{o.ScriptPath(): RenderZsh(o.BinPath)}, nil
	case Bash:
		preexec := filepath.Join(o.ScriptDir, preexecName)
		return map[string]string{o.ScriptPath(): RenderBash(o.BinPath, preexec), preexec: BashPreexec}, nil
	case Fish:
		return map[string]string{o.ScriptPath(): RenderFish(o.BinPath)}, nil
	default:
		return nil, fmt.Errorf("shell: unsupported shell %q", o.Shell)
	}
}

// Install writes the hook script and adds (or refreshes) the marked block
// in the rc file. Running it again is a no-op apart from refreshing the
// script.
func Install(o InstallOptions) error {
	if (o.RCPath == "" && o.kind() != Fish) || o.ScriptDir == "" || o.BinPath == "" || o.BackupDir == "" || o.Now == nil {
		return errors.New("shell: incomplete install options")
	}
	files, err := o.scripts()
	if err != nil {
		return err
	}
	for path, content := range files {
		// Owner-only: the user's own shell sources it, nobody else needs to read it.
		if err := fileedit.WriteAtomic(path, []byte(content), 0o600); err != nil {
			return fmt.Errorf("shell: write hook script: %w", err)
		}
	}
	if o.RCPath == "" {
		return nil
	}
	return editRC(o.RCPath, o.BackupDir, o.Now(), func(content []byte) []byte {
		return withBlock(content, block(o.kind(), o.ScriptPath()))
	})
}

// Uninstall removes exactly the marked block (and the blank line Install
// added before it) and deletes the hook scripts.
func Uninstall(o InstallOptions) error {
	if (o.RCPath == "" && o.kind() != Fish) || o.BackupDir == "" || o.Now == nil {
		return errors.New("shell: incomplete install options")
	}
	if o.RCPath != "" {
		if err := editRC(o.RCPath, o.BackupDir, o.Now(), withoutBlock); err != nil {
			return err
		}
	}
	if o.ScriptDir == "" {
		return nil
	}
	files, err := o.scripts()
	if err != nil {
		return err
	}
	for path := range files {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("shell: remove hook script: %w", err)
		}
	}
	return nil
}

func editRC(rcPath, backupDir string, now time.Time, edit func([]byte) []byte) error {
	target, err := fileedit.ResolveTarget(rcPath)
	if err != nil {
		return fmt.Errorf("shell: resolve %s: %w", rcPath, err)
	}
	old, mode, existed, err := fileedit.ReadExisting(target, 0o644)
	if err != nil {
		return fmt.Errorf("shell: read %s: %w", target, err)
	}
	updated := edit(old)
	if bytes.Equal(updated, old) && (existed || len(updated) == 0) {
		return nil
	}
	if existed {
		if _, err := fileedit.Backup(backupDir, target, old, now); err != nil {
			return err
		}
	}
	if err := fileedit.WriteAtomic(target, updated, mode); err != nil {
		return fmt.Errorf("shell: write %s: %w", target, err)
	}
	return nil
}

// block is the marked block for an rc file: it sources the hook in
// interactive shells only.
func block(k Kind, scriptPath string) []byte {
	q := fileedit.ShellQuote(scriptPath)
	interactive := "[[ -o interactive ]]"
	if k == Bash {
		interactive = "[[ $- == *i* ]]"
	}
	return []byte(blockBegin + interactive + " && [[ -r " + q + " ]] && source " + q + "\n" + blockEnd)
}

// findBlock returns the byte range of our block, including a missing final
// newline, or -1 if there is none. Markers only count at the start of a line.
func findBlock(content []byte) (start, end int) {
	start = indexAtLineStart(content, []byte(blockBegin))
	if start < 0 {
		return -1, -1
	}
	rel := indexAtLineStart(content[start:], []byte(blockEnd[:len(blockEnd)-1]))
	if rel < 0 {
		return -1, -1
	}
	end = start + rel + len(blockEnd) - 1
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return start, end
}

func indexAtLineStart(s, sub []byte) int {
	for off := 0; ; {
		i := bytes.Index(s[off:], sub)
		if i < 0 {
			return -1
		}
		if at := off + i; at == 0 || s[at-1] == '\n' {
			return at
		}
		off += i + 1
	}
}

// withBlock replaces an existing block in place, or appends one separated
// from the user's content by a blank line.
func withBlock(content, blk []byte) []byte {
	if start, end := findBlock(content); start >= 0 {
		return join(content[:start], blk, content[end:])
	}
	if len(content) == 0 {
		return blk
	}
	out := bytes.Clone(content)
	if out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return join(out, []byte("\n"), blk)
}

// withoutBlock removes our block and the separating blank line before it.
func withoutBlock(content []byte) []byte {
	start, end := findBlock(content)
	if start < 0 {
		return content
	}
	if start >= 2 && content[start-1] == '\n' && content[start-2] == '\n' {
		start--
	}
	return join(content[:start], content[end:])
}

func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
