package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/claude"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/shell"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

type hookTargets struct {
	zsh, bash, fish, claude bool
}

// parseHookFlags reads which hooks to (un)install. Installing bash and
// fish hooks defaults to on only for shells in use (their config exists, or
// it is the login shell), so no dotfiles are created for shells you don't
// use. Uninstalling defaults to every shell; removing a hook that isn't
// there does nothing.
func parseHookFlags(name string, args []string, install bool) (hookTargets, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	t := hookTargets{}
	fs.BoolVar(&t.zsh, "zsh", true, "zsh hook in ~/.zshrc (or $ZDOTDIR/.zshrc)")
	fs.BoolVar(&t.bash, "bash", !install || shellInUse(shell.Bash), "bash hook in ~/.bashrc (default: if you use bash)")
	fs.BoolVar(&t.fish, "fish", !install || shellInUse(shell.Fish), "fish hook in fish's conf.d (default: if you use fish)")
	fs.BoolVar(&t.claude, "claude", true, "Claude Code hooks in ~/.claude/settings.json")
	return t, fs.Parse(args)
}

func runInstallHooks(ctx context.Context, args []string) error {
	t, err := parseHookFlags("install-hooks", args, true)
	if err != nil {
		return err
	}
	bin, err := binaryPath()
	if err != nil {
		return err
	}
	if err := paths.CheckTrustedExecutable(bin); err != nil {
		return err
	}
	var errs []error
	for _, k := range t.shells() {
		errs = append(errs, report(string(k)+" hook", installShell(k, bin, true)))
	}
	if t.claude {
		errs = append(errs, report("Claude Code hooks", installClaude(ctx, bin, true)))
	}
	return errors.Join(errs...)
}

func runUninstallHooks(ctx context.Context, args []string) error {
	t, err := parseHookFlags("uninstall-hooks", args, false)
	if err != nil {
		return err
	}
	bin, err := binaryPath()
	if err != nil {
		return err
	}
	var errs []error
	for _, k := range t.shells() {
		errs = append(errs, report(string(k)+" hook removed", installShell(k, bin, false)))
	}
	if t.claude {
		errs = append(errs, report("Claude Code hooks removed", installClaude(ctx, bin, false)))
	}
	return errors.Join(errs...)
}

func (t hookTargets) shells() []shell.Kind {
	var out []shell.Kind
	for k, on := range map[shell.Kind]bool{shell.Zsh: t.zsh, shell.Bash: t.bash, shell.Fish: t.fish} {
		if on {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// shellInUse reports whether k is the login shell or has its config.
func shellInUse(k shell.Kind) bool {
	if filepath.Base(os.Getenv("SHELL")) == string(k) {
		return true
	}
	o, err := shellOptions(k, "")
	if err != nil {
		return false
	}
	probe := o.RCPath
	if k == shell.Fish {
		probe = filepath.Dir(o.ScriptDir) // ~/.config/fish
	}
	_, err = os.Stat(probe)
	return err == nil
}

// shellOptions says where k's hook goes.
func shellOptions(k shell.Kind, bin string) (shell.InstallOptions, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return shell.InstallOptions{}, err
	}
	cfg, err := paths.ConfigDir()
	if err != nil {
		return shell.InstallOptions{}, err
	}
	backups, err := hookBackupDir()
	if err != nil {
		return shell.InstallOptions{}, err
	}
	o := shell.InstallOptions{
		Shell: k, ScriptDir: filepath.Join(cfg, "hooks"), BinPath: bin, BackupDir: backups, Now: time.Now,
	}
	switch k {
	case shell.Zsh:
		rcDir := home
		if z := os.Getenv("ZDOTDIR"); z != "" {
			rcDir = z
		}
		o.RCPath = filepath.Join(rcDir, ".zshrc")
	case shell.Bash:
		o.RCPath = filepath.Join(home, ".bashrc")
	case shell.Fish:
		// fish reads $XDG_CONFIG_HOME/fish, whatever burnout's own
		// overrides say; conf.d files load without editing config.fish.
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		o.ScriptDir = filepath.Join(base, "fish", "conf.d")
	}
	return o, nil
}

func installShell(k shell.Kind, bin string, install bool) error {
	o, err := shellOptions(k, bin)
	if err != nil {
		return err
	}
	if install {
		return shell.Install(o)
	}
	return shell.Uninstall(o)
}

func installClaude(ctx context.Context, bin string, install bool) error {
	settings, err := claude.DefaultSettingsPath()
	if err != nil {
		return err
	}
	backups, err := hookBackupDir()
	if err != nil {
		return err
	}
	o := claude.InstallOptions{
		SettingsPath: settings,
		Command:      claude.CommandFor(bin),
		Version:      claude.DetectVersion(ctx),
		BackupDir:    backups,
		Now:          time.Now,
	}
	if install {
		return claude.Install(o)
	}
	return claude.Uninstall(o)
}

// binaryPath is the absolute path hooks and the service will run. It is
// the path burnout was started by, symlinks kept, so a stable link (for
// example one a package manager updates) survives upgrades; on Linux,
// os.Executable alone would resolve the link.
func binaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := invokedPath(); err == nil && sameFile(p, exe) {
		return p, nil
	}
	return filepath.Abs(exe)
}

// invokedPath is os.Args[0] made absolute, looked up on PATH when it was
// run by name.
func invokedPath() (string, error) {
	p := os.Args[0]
	if !strings.ContainsAny(p, `/\`) {
		var err error
		if p, err = exec.LookPath(p); err != nil {
			return "", err
		}
	}
	return filepath.Abs(p)
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// hookBackupDir is where installers back up files before editing them:
// the app's own data directory, never next to the user's file.
func hookBackupDir() (string, error) {
	dir, err := paths.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hook-backups"), nil
}

func report(what string, err error) error {
	if err != nil {
		fmt.Printf("  FAIL %s: %v\n", what, err)
		return err
	}
	fmt.Printf("  ok   %s\n", what)
	return nil
}
