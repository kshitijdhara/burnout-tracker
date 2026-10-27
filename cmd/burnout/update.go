package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/egress/update"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
)

// runUpdate is `burnout update`: find, verify and install a release
// (ADR-0005). It is one of the three opt-in network calls (privacy &
// security §6) and only runs when the user asks.
func runUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	channel := fs.String("channel", string(update.Stable), "`stable`, or beta to include pre-releases")
	to := fs.String("to", "", "install this `version` (e.g. v1.2.3) instead of the newest; with --allow-downgrade, rolls back")
	allowDowngrade := fs.Bool("allow-downgrade", false, "allow a version that isn't newer than this one")
	checkOnly := fs.Bool("check", false, "only say whether a newer release exists")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: burnout update [--check] [--channel stable|beta] [--to vX.Y.Z] [--allow-downgrade]")
	}
	ch := update.Channel(*channel)
	if ch != update.Stable && ch != update.Beta {
		return fmt.Errorf("--channel %q: want stable or beta", *channel)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := update.Detect(exe, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, update.Timeout)
	defer cancel()
	fmt.Println("Checking GitHub for burnout releases…")
	res, err := update.New().Run(ctx, update.Options{
		Current:        version,
		Channel:        ch,
		To:             *to,
		AllowDowngrade: *allowDowngrade,
		CheckOnly:      *checkOnly,
		Install:        in,
		DownloadDir:    filepath.Join(cache, "burnout", "updates"),
	})
	if errors.Is(err, update.ErrNoRelease) {
		return errors.New("no such release on GitHub yet")
	}
	if err != nil {
		return err
	}
	reportUpdate(os.Stdout, in, res, *checkOnly)
	return nil
}

// reportUpdate tells the user what happened and what is left for them.
func reportUpdate(w io.Writer, in update.Install, res update.Result, checkOnly bool) {
	v := res.Version
	switch {
	case res.UpToDate:
		_, _ = fmt.Fprintf(w, "burnout %s is the newest release.\n", version)
	case checkOnly:
		_, _ = fmt.Fprintf(w, "burnout %s is available (this is %s). Run `burnout update` to install it.\n", v, version)
	case res.Swapped:
		_, _ = fmt.Fprintf(w, "Updated %s to %s (signature and checksum verified).\n", version, v)
		_, _ = fmt.Fprintf(w, "The previous binary is kept at %s.\n", printable(res.Previous))
		_, _ = fmt.Fprintln(w, "A running daemon keeps the old version until it restarts: run `burnout service install` to restart it now.")
	default:
		_, _ = fmt.Fprintf(w, "Downloaded burnout %s and verified its signature and checksum:\n  %s\n", v, printable(res.Download))
		_, _ = fmt.Fprintf(w, "This copy is a %s (%s), so burnout doesn't replace it itself. To install it:\n  %s\n",
			in.Kind, printable(in.Path), installHint(in, res.Download))
	}
}

// installHint is how the user installs a downloaded package.
func installHint(in update.Install, path string) string {
	switch {
	case in.Kind == update.App:
		return "open " + path + " and drag Burnout.app to Applications, replacing the old one"
	case strings.HasSuffix(path, ".deb"):
		return "sudo apt install " + path
	case strings.HasSuffix(path, ".rpm"):
		return "sudo rpm -U " + path
	case strings.HasSuffix(path, ".zip"):
		return "unzip it and replace " + in.Path + " with its burnout.exe"
	}
	return "unpack it and replace " + in.Path + " with its burnout binary"
}

// The weekly check's timing in the daemon.
const (
	updateFirst = 10 * time.Minute // after start, so a login isn't slowed
	updateWake  = time.Hour        // how often the loop looks at whether a check is due
)

// updateLoop runs the opt-in weekly update check (update.check = "weekly")
// from the daemon. It only asks GitHub for the newest version and records
// it for the tray and doctor; it never downloads or installs anything.
func updateLoop(ctx context.Context, log *slog.Logger, dataDir string, live *liveConfig) {
	updateLoopWith(ctx, log, dataDir, live, update.New().Check, updateFirst, updateWake)
}

func updateLoopWith(ctx context.Context, log *slog.Logger, dataDir string, live *liveConfig,
	check func(context.Context) (update.Version, error), first, wake time.Duration,
) {
	wait := first
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = wake
		now := time.Now()
		st := update.LoadState(dataDir)
		if live.current().Update.Check != config.UpdateWeekly || !st.Due(now) {
			continue
		}
		st.Tried = now
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		v, err := check(cctx)
		cancel()
		if err != nil {
			log.Warn("update check failed", "err", err)
		} else {
			st.Checked, st.Latest = now, v.String()
			log.Info("update check done", "latest", st.Latest)
		}
		if err := update.SaveState(dataDir, st); err != nil {
			log.Warn("could not save the update check", "err", err)
		}
	}
}

// updateNotice is a newer release found by the weekly check, or "" when
// there is none or the check is off.
func updateNotice(dataDir string, c config.Config) string {
	if c.Update.Check != config.UpdateWeekly {
		return ""
	}
	if v, ok := update.LoadState(dataDir).Newer(version); ok {
		return "burnout " + v.String() + " is available: run `burnout update`"
	}
	return ""
}

// updateLine is the update check's one-line summary, for privacy and
// doctor's network section.
func updateLine(dataDir string, c config.Config) string {
	if c.Update.Check != config.UpdateWeekly {
		return "off (`burnout update` checks only when you run it; `burnout config set update.check weekly` checks weekly)"
	}
	st := update.LoadState(dataDir)
	s := "weekly, to " + strings.Join(update.Hosts, ", ")
	if st.Checked.IsZero() {
		return s + "; not checked yet"
	}
	return s + "; last checked " + st.Checked.Local().Format("2006-01-02")
}
