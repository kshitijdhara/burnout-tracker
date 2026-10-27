package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/wayland"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/export"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/service"
)

// runUninstall removes burnout's hooks and background service, and with
// --purge everything it stored: data folder, config folder, keystore
// entries and downloaded updates (privacy & security §7.2). It asks
// first, and only in a terminal: there is no flag to skip the question.
func runUninstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete all recorded data, the config and the stored keys")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("uninstall must be run interactively in a terminal")
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	cfgDir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	exe, _ := os.Executable() // only for a hint
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved // update.Swap keeps <binary>.old beside the resolved path
	}
	return uninstall(ctx, os.Stdin, os.Stdout, *purge, uninstallDirs{
		data: dataDir, config: cfgDir, cache: filepath.Join(cache, paths.AppName), exe: exe, goos: runtime.GOOS,
	}, uninstallSteps{
		service: func(ctx context.Context) error {
			m, _, err := serviceManager()
			if err == nil {
				err = uninstallService(ctx, m)
			}
			if errors.Is(err, service.ErrUnsupported) {
				return nil // nothing installed on this platform
			}
			return err
		},
		hooks:   func(ctx context.Context) error { return runUninstallHooks(ctx, nil) },
		running: daemonAnswers,
		keys: func() error {
			keys, err := keystore.New(dataDir, keystoreOptions()...)
			if err != nil {
				return err
			}
			return errors.Join(keys.Delete(hmacKeyName), keys.Delete(icsKey))
		},
	})
}

// uninstallDirs are where burnout keeps things. cache holds the updates
// `burnout update` downloads; exe is the running binary, which may have
// an update's leftover beside it.
type uninstallDirs struct {
	data, config, cache, exe, goos string
}

// uninstallSteps are the parts of an uninstall that touch the system, so
// tests can replace them.
type uninstallSteps struct {
	service func(context.Context) error
	hooks   func(context.Context) error
	running func(context.Context) bool
	keys    func() error
}

func uninstall(ctx context.Context, in io.Reader, out io.Writer, purge bool, d uninstallDirs, s uninstallSteps) error {
	word := "uninstall"
	_, _ = fmt.Fprintln(out, "This removes burnout's shell and Claude Code hooks and its background service.")
	if purge {
		word = "delete"
		_, _ = fmt.Fprintf(out, "It also deletes, for good:\n  %s (everything recorded)\n  %s (settings and consent)\n  %s (downloaded updates)\n  burnout's keys in the OS keystore\n",
			printable(d.data), printable(d.config), printable(d.cache))
		// The record of exports goes with the data folder, so this is
		// the last chance to see where they are (privacy & security §7.2).
		if left, err := export.Existing(d.data); err == nil && len(left) > 0 {
			_, _ = fmt.Fprintln(out, "It can't delete the exports you saved; delete them yourself if they should go too:")
			for _, e := range left {
				_, _ = fmt.Fprintf(out, "  %s  (%s)\n", printable(e.Path), e.At.Format("2006-01-02 15:04"))
			}
		}
	} else {
		_, _ = fmt.Fprintf(out, "Your data stays in %s; `burnout uninstall --purge` deletes it.\n", printable(d.data))
	}
	_, _ = fmt.Fprintf(out, "Type %q to go ahead: ", word)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(line) != word {
		_, _ = fmt.Fprintln(out, "Nothing was changed.")
		return nil
	}
	if err := errors.Join(s.service(ctx), s.hooks(ctx)); err != nil {
		return fmt.Errorf("uninstall stopped before deleting anything: %w", err)
	}
	if !purge {
		_, _ = fmt.Fprintln(out, "Uninstalled. Remove the burnout binary yourself if you no longer want it.")
		leftovers(out, d)
		return nil
	}
	if s.running(ctx) {
		return errors.New("a burnout daemon is still running, probably in a terminal: stop it (Ctrl-C), then run `burnout uninstall --purge` again; nothing was deleted")
	}
	// Keys first: the file keystore lives in the data folder.
	if err := s.keys(); err != nil {
		return fmt.Errorf("could not delete the stored keys; nothing else was deleted: %w", err)
	}
	if err := errors.Join(os.RemoveAll(d.data), os.RemoveAll(d.config), os.RemoveAll(d.cache)); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Uninstalled, and all of burnout's data, settings and keys are deleted. Remove the burnout binary yourself.")
	leftovers(out, d)
	return nil
}

// leftovers points at what uninstall can't remove itself: the binary an
// update replaced, and on Linux the GNOME Shell extension, which the
// package installed and only the user can turn off.
func leftovers(out io.Writer, d uninstallDirs) {
	if d.exe != "" {
		if old := d.exe + ".old"; fileExists(old) {
			_, _ = fmt.Fprintf(out, "The binary an update replaced is still at %s; remove it too.\n", printable(old))
		}
	}
	if d.goos == "linux" {
		_, _ = fmt.Fprintf(out, "If you turned on the GNOME Shell extension, turn it off with `gnome-extensions disable %s`, then remove the burnout package.\n", wayland.UUID)
	}
}

func fileExists(p string) bool { _, err := os.Lstat(p); return err == nil }
