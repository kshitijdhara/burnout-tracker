package shell

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func opts(t *testing.T, rc string) InstallOptions {
	t.Helper()
	return InstallOptions{
		RCPath:    rc,
		ScriptDir: filepath.Join(t.TempDir(), "hooks"),
		BinPath:   "/opt/burn out/bin/burnout", // a space and nothing else exotic
		BackupDir: backupDirFor(rc),
		Now:       func() time.Time { return time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC) },
	}
}

// backupDirFor keeps backups in their own directory, away from the rc file.
func backupDirFor(rc string) string { return filepath.Join(filepath.Dir(rc), "backups") }

func backupsOf(rc string) []string {
	b, _ := filepath.Glob(filepath.Join(backupDirFor(rc), "*.bak"))
	return b
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallUninstallRoundTrip(t *testing.T) {
	originals := []string{
		"",
		"export PATH=$HOME/bin:$PATH\n",
		"alias g=git\n\n# comment\nsetopt autocd\n",
		"# a burnout >>> mention that is not our marker\n",
	}
	for _, orig := range originals {
		t.Run(strings.SplitN(orig, "\n", 2)[0], func(t *testing.T) {
			rc := filepath.Join(t.TempDir(), ".zshrc")
			if orig != "" {
				if err := os.WriteFile(rc, []byte(orig), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			o := opts(t, rc)
			if err := Install(o); err != nil {
				t.Fatal(err)
			}
			installed := read(t, rc)
			if !strings.HasPrefix(installed, orig) {
				t.Fatalf("user content not preserved:\n%s", installed)
			}
			if strings.Count(installed, blockBegin) != 1 || !strings.Contains(installed, "source '"+o.ScriptPath()+"'") {
				t.Fatalf("block missing or wrong:\n%s", installed)
			}

			// Idempotent: a second install changes nothing.
			if err := Install(o); err != nil {
				t.Fatal(err)
			}
			if again := read(t, rc); again != installed {
				t.Fatalf("second install changed the file:\n%s", again)
			}

			if err := Uninstall(o); err != nil {
				t.Fatal(err)
			}
			if orig == "" {
				if got := read(t, rc); got != "" {
					t.Fatalf("uninstall left %q", got)
				}
			} else if got := read(t, rc); got != orig {
				t.Fatalf("uninstall did not restore exactly:\ngot  %q\nwant %q", got, orig)
			}
			if _, err := os.Stat(o.ScriptPath()); !os.IsNotExist(err) {
				t.Fatal("hook script not removed")
			}
		})
	}
}

func TestInstallPreservesModeAndBacksUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are advisory on Windows")
	}
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(rc, []byte("setopt autocd\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Install(opts(t, rc)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(rc)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", fi.Mode().Perm())
	}
	backups := backupsOf(rc)
	if len(backups) != 1 || read(t, backups[0]) != "setopt autocd\n" {
		t.Fatalf("backups = %v", backups)
	}
	if bi, _ := os.Stat(backups[0]); bi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v", bi.Mode().Perm())
	}
}

func TestInstallFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "zshrc")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("setopt autocd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".zshrc")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	o := opts(t, link)
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a regular file")
	}
	if !strings.Contains(read(t, target), blockBegin) {
		t.Fatal("target file not updated")
	}
	if err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if got := read(t, target); got != "setopt autocd\n" {
		t.Fatalf("got %q", got)
	}
}

func TestInstallReplacesMovedBlockInPlace(t *testing.T) {
	rc := filepath.Join(t.TempDir(), ".zshrc")
	o := opts(t, rc)
	content := "a\n" + blockBegin + "old line\n" + blockEnd + "b\n"
	if err := os.WriteFile(rc, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	got := read(t, rc)
	if !strings.HasPrefix(got, "a\n"+blockBegin) || !strings.HasSuffix(got, blockEnd+"b\n") || strings.Contains(got, "old line") {
		t.Fatalf("block not replaced in place:\n%s", got)
	}
}

func TestUninstallWithoutRCIsNoop(t *testing.T) {
	rc := filepath.Join(t.TempDir(), ".zshrc")
	if err := Uninstall(opts(t, rc)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rc); !os.IsNotExist(err) {
		t.Fatal("uninstall created an rc file")
	}
}

func TestRenderQuotesBinary(t *testing.T) {
	got := RenderZsh("/a/it's here/burnout")
	if !strings.Contains(got, `__burnout_bin='/a/it'\''s here/burnout'`) {
		t.Fatalf("binary not safely quoted:\n%s", got)
	}
	if strings.Contains(got, binPlaceholder) || strings.Contains(got, subcmdsPlaceholder) {
		t.Fatal("placeholder left in script")
	}
	if !strings.Contains(got, "  git ' add am ") {
		t.Fatal("subcommand table not rendered")
	}
}
