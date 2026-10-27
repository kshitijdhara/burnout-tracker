// Command burnout is the single entry point: the daemon, the hot-path hook
// subcommands, and the CLI.
//
// Hook subcommands (emit, hook) must never write to stdout or stderr and
// must always exit 0: Claude Code adds hook stdout to the model's context,
// and a failing shell hook would get in the user's way.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // calendar feeds name time zones; Windows has no zone database
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type command struct {
	name, usage string
	run         func(ctx context.Context, args []string) error
}

func commands() []command {
	return []command{
		{"setup", "interactive first-run: review what is collected and consent", runSetup},
		{"daemon", "run the collector daemon in this terminal (the service runs it in the background)", runDaemon},
		{"service", "run the daemon in the background from login: service install|uninstall|status", runService},
		{"status", "show today's summary", runStatus},
		{"dashboard", "open the local dashboard in your browser (data stays on this device)", runDashboard},
		{"timeline", "show a day minute by minute: timeline [YYYY-MM-DD]", runTimeline},
		{"pause", "stop recording for a while: pause [1h | tomorrow] (default: until resume)", runPause},
		{"resume", "start recording again after a pause", runResume},
		{"forget", "delete recorded data: forget --last 1h | --today | --since DATE | --all", runForget},
		{"annotate", "mark days: annotate vacation FROM [TO] [--note TEXT] | list | remove ID", runAnnotate},
		{"boundary", "rules for your work: boundary add shutdown 21:30 | no-weekend | meetings-max 4 | focus-block 90; list; remove", runBoundary},
		{"privacy", "what is collected, your current settings and your controls", runPrivacy},
		{"config", "settings: config get|set|add|remove|unset|validate|path", runConfig},
		{"calendar", "meetings from your calendar feed: calendar add URL|FILE | sync | status | remove", runCalendar},
		{"export", "write your data out as JSON or CSV: export --out FILE [--format csv] [--since DATE]", runExport},
		{"checkin", "answer the weekly check-in (or --daily for two quick questions)", runCheckin},
		{"review", "the weekly review: this week against the last, and what to try", runReview},
		{"update", "install the newest release, verified: update [--check] [--channel beta] [--to vX.Y.Z] [--allow-downgrade]", runUpdate},
		{"doctor", "check permissions, paths, disk encryption and collectors", runDoctor},
		{"install-hooks", "install shell (zsh) and Claude Code hooks", runInstallHooks},
		{"uninstall-hooks", "remove shell and Claude Code hooks", runUninstallHooks},
		{"uninstall", "remove hooks and the background service: uninstall [--purge] (--purge also deletes your data)", runUninstall},
		{"version", "print the version", func(context.Context, []string) error { fmt.Println(version); return nil }},
	}
}

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "emit":
			runEmit(os.Args[2:]) // never returns an error, never prints
			return
		case "hook":
			runHook(os.Args[2:])
			return
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	for _, c := range commands() {
		if c.name == os.Args[1] {
			if err := c.run(ctx, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "burnout:", err)
				os.Exit(1)
			}
			return
		}
	}
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: burnout <command> [args]")
	fmt.Fprintln(os.Stderr)
	for _, c := range commands() {
		fmt.Fprintf(os.Stderr, "  %-16s %s\n", c.name, c.usage)
	}
}
