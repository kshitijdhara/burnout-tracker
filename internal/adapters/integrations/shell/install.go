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
	scriptName = "burnout.zsh"
)

// InstallOptions says where the hook goes.
type InstallOptions struct {
	RCPath    string // e.g. ~/.zshrc; may be a symlink
	ScriptDir string // where the rendered script is written, e.g. <config dir>/hooks
	BinPath   string // absolute path of the burnout binary
	BackupDir string // where the pre-edit copy of the rc file goes (the app's private data dir)
	Now       func() time.Time
}

// ScriptPath is where Install writes the rendered hook script.
func (o InstallOptions) ScriptPath() string { return filepath.Join(o.ScriptDir, scriptName) }

// Install writes the hook script and adds (or refreshes) the marked block
// in the rc file. Running it again is a no-op apart from refreshing the
// script.
func Install(o InstallOptions) error {
	if o.RCPath == "" || o.ScriptDir == "" || o.BinPath == "" || o.BackupDir == "" || o.Now == nil {
		return errors.New("shell: incomplete install options")
	}
	// Owner-only: the user's own shell sources it, nobody else needs to read it.
	if err := fileedit.WriteAtomic(o.ScriptPath(), []byte(RenderZsh(o.BinPath)), 0o600); err != nil {
		return fmt.Errorf("shell: write hook script: %w", err)
	}
	return editRC(o.RCPath, o.BackupDir, o.Now(), func(content []byte) []byte {
		return withBlock(content, block(o.ScriptPath()))
	})
}

// Uninstall removes exactly the marked block (and the blank line Install
// added before it) and deletes the hook script.
func Uninstall(o InstallOptions) error {
	if o.RCPath == "" || o.BackupDir == "" || o.Now == nil {
		return errors.New("shell: incomplete install options")
	}
	if err := editRC(o.RCPath, o.BackupDir, o.Now(), withoutBlock); err != nil {
		return err
	}
	if o.ScriptDir != "" {
		if err := os.Remove(o.ScriptPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
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

func block(scriptPath string) []byte {
	q := fileedit.ShellQuote(scriptPath)
	return []byte(blockBegin + "[[ -o interactive ]] && [[ -r " + q + " ]] && source " + q + "\n" + blockEnd)
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
