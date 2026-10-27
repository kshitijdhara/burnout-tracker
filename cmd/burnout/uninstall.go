package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/service"
)

// runUninstall removes burnout's hooks and background service, and with
// --purge everything it stored: data folder, config folder and keystore
// entries (privacy & security §7.2). It asks first, and only in a
// terminal: there is no flag to skip the question.
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
	return uninstall(ctx, os.Stdin, os.Stdout, *purge, dataDir, cfgDir, uninstallSteps{
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

// uninstallSteps are the parts of an uninstall that touch the system, so
// tests can replace them.
type uninstallSteps struct {
	service func(context.Context) error
	hooks   func(context.Context) error
	running func(context.Context) bool
	keys    func() error
}

func uninstall(ctx context.Context, in io.Reader, out io.Writer, purge bool, dataDir, cfgDir string, s uninstallSteps) error {
	word := "uninstall"
	_, _ = fmt.Fprintln(out, "This removes burnout's shell and Claude Code hooks and its background service.")
	if purge {
		word = "delete"
		_, _ = fmt.Fprintf(out, "It also deletes, for good:\n  %s (everything recorded)\n  %s (settings and consent)\n  burnout's keys in the OS keystore\n", dataDir, cfgDir)
	} else {
		_, _ = fmt.Fprintf(out, "Your data stays in %s; `burnout uninstall --purge` deletes it.\n", dataDir)
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
		return nil
	}
	if s.running(ctx) {
		return errors.New("a burnout daemon is still running, probably in a terminal: stop it (Ctrl-C), then run `burnout uninstall --purge` again; nothing was deleted")
	}
	// Keys first: the file keystore lives in the data folder.
	if err := s.keys(); err != nil {
		return fmt.Errorf("could not delete the stored keys; nothing else was deleted: %w", err)
	}
	if err := errors.Join(os.RemoveAll(dataDir), os.RemoveAll(cfgDir)); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Uninstalled, and all of burnout's data, settings and keys are deleted. Remove the burnout binary yourself.")
	return nil
}
