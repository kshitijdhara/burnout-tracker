package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/ingest"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/claude"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/shell"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/procinfo"
	"github.com/kshitijdhara/burnout-tracker/internal/wire"
)

// sendBudget bounds how long a hook may spend talking to the daemon.
const sendBudget = 50 * time.Millisecond

func send(m wire.Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), sendBudget)
	defer cancel()
	return ingest.Send(ctx, m)
}

// hookGuard makes a hook process silent and infallible: stdout/stderr go
// to the null device and any panic is swallowed. Exit status is always 0.
// It also ignores SIGHUP: fish starts the emitter in its own process group,
// which is hung up when the shell exits, and closing a terminal right after
// a command must not lose that command.
func hookGuard() func() {
	signal.Ignore(syscall.SIGHUP)
	silenceStdio()
	return func() { _ = recover() }
}

// runEmit handles `burnout emit shell`, called by the shell hook. All data
// arrives in environment variables, never in argv (privacy & security §4.2).
func runEmit(args []string) {
	defer hookGuard()()
	if len(args) != 1 || args[0] != "shell" {
		return
	}
	defer holdForTest()
	cwd, _ := os.Getwd()
	m, err := shell.EmitFromEnv(os.Getenv, cwd, time.Now())
	if err != nil {
		return
	}
	_ = send(m)
}

// envEmitHold, set to a duration of at most a second, keeps each emitter
// running that long after it has sent. TestPSLeak uses it so its process
// sampler reliably sees emitters on a busy CI runner; an emitter normally
// lives a few milliseconds. Emitters run in the background, so setting it
// delays nothing the user waits on.
const envEmitHold = "BURNOUT_TEST_EMIT_HOLD"

func holdForTest() {
	if d, err := time.ParseDuration(os.Getenv(envEmitHold)); err == nil && d > 0 && d <= time.Second {
		time.Sleep(d)
	}
}

// runHook handles `burnout hook claude`, called by Claude Code for every
// registered hook event, with the event payload on stdin.
func runHook(args []string) {
	defer hookGuard()()
	if len(args) != 1 || args[0] != claude.AgentName {
		// Unknown agent: drain stdin so the caller never sees a broken pipe.
		claude.Drain(os.Stdin)
		return
	}
	claude.Run(os.Stdin, time.Now, send, procinfo.Info{})
}
