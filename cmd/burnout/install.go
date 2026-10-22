package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/claude"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/shell"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

type hookTargets struct {
	zsh, claude bool
}

func parseHookFlags(name string, args []string) (hookTargets, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	t := hookTargets{}
	fs.BoolVar(&t.zsh, "zsh", true, "zsh hook in ~/.zshrc (or $ZDOTDIR/.zshrc)")
	fs.BoolVar(&t.claude, "claude", true, "Claude Code hooks in ~/.claude/settings.json")
	return t, fs.Parse(args)
}

func runInstallHooks(ctx context.Context, args []string) error {
	t, err := parseHookFlags("install-hooks", args)
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
	if t.zsh {
		errs = append(errs, report("zsh hook", installZsh(bin, true)))
	}
	if t.claude {
		errs = append(errs, report("Claude Code hooks", installClaude(ctx, bin, true)))
	}
	return errors.Join(errs...)
}

func runUninstallHooks(ctx context.Context, args []string) error {
	t, err := parseHookFlags("uninstall-hooks", args)
	if err != nil {
		return err
	}
	bin, err := binaryPath()
	if err != nil {
		return err
	}
	var errs []error
	if t.zsh {
		errs = append(errs, report("zsh hook removed", installZsh(bin, false)))
	}
	if t.claude {
		errs = append(errs, report("Claude Code hooks removed", installClaude(ctx, bin, false)))
	}
	return errors.Join(errs...)
}

func installZsh(bin string, install bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rcDir := home
	if z := os.Getenv("ZDOTDIR"); z != "" {
		rcDir = z
	}
	cfg, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	backups, err := hookBackupDir()
	if err != nil {
		return err
	}
	o := shell.InstallOptions{
		RCPath:    filepath.Join(rcDir, ".zshrc"),
		ScriptDir: filepath.Join(cfg, "hooks"),
		BinPath:   bin,
		BackupDir: backups,
		Now:       time.Now,
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
