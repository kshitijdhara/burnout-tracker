// Package shell installs the interactive-shell hooks and turns what they
// emit into wire messages: zsh, bash (3.2+) and fish.
//
// The hook script does the privacy-critical work inside the shell: the
// command line never leaves the hook function. See
// docs/privacy-and-security.md §4.2 and docs/adr/0003-instrumentation-via-hooks.md.
package shell

import (
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/integrations/fileedit"

	"github.com/kshitijdhara/burntout/internal/wire"
)

var (
	//go:embed hooks/burntout.zsh
	zshScript string
	//go:embed hooks/burntout.bash
	bashScript string
	//go:embed hooks/burntout.fish
	fishScript string
	// BashPreexec is the vendored bash-preexec library (hooks/README.md).
	//go:embed hooks/bash-preexec.sh
	BashPreexec string
)

const (
	binPlaceholder     = "__BURNTOUT_BIN__"
	subcmdsPlaceholder = "__BURNTOUT_SUBCMDS__"
	preexecPlaceholder = "__BURNTOUT_PREEXEC__"
)

// Environment variables the hook sets for `burntout emit shell`.
const (
	EnvArgv0    = "BURNTOUT_ARGV0"
	EnvSubcmd   = "BURNTOUT_SUBCMD"
	EnvExit     = "BURNTOUT_EXIT"
	EnvStart    = "BURNTOUT_T0"
	EnvEnd      = "BURNTOUT_T1"
	EnvTTY      = "BURNTOUT_TTY"
	EnvShellPID = "BURNTOUT_SHELL_PID"
	// EnvDuration replaces EnvStart and EnvEnd for shells without a
	// millisecond clock (bash before 5, fish): the command ended now and
	// took this many milliseconds.
	EnvDuration = "BURNTOUT_DURATION_MS"
)

// RenderZsh returns the zsh hook script with binPath and the subcommand
// allowlist substituted.
func RenderZsh(binPath string) string {
	s := strings.Replace(zshScript, binPlaceholder, fileedit.ShellQuote(binPath), 1)
	return strings.Replace(s, subcmdsPlaceholder, zshSubcommandTable(), 1)
}

// RenderBash returns the bash hook script with binPath, the path of the
// bash-preexec copy to load if no other tool has, and the subcommand
// allowlist substituted.
func RenderBash(binPath, preexecPath string) string {
	s := strings.Replace(bashScript, binPlaceholder, fileedit.ShellQuote(binPath), 1)
	s = strings.Replace(s, preexecPlaceholder, fileedit.ShellQuote(preexecPath), 1)
	return strings.Replace(s, subcmdsPlaceholder, bashSubcommandFuncs(), 1)
}

// RenderFish returns the fish hook script with binPath and the subcommand
// allowlist substituted.
func RenderFish(binPath string) string {
	s := strings.Replace(fishScript, binPlaceholder, fishQuote(binPath), 1)
	return strings.Replace(s, subcmdsPlaceholder, fishSubcommandFuncs(), 1)
}

// fishQuote quotes s for fish, where only \\ and \' are special inside
// single quotes.
func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// EmitFromEnv builds the wire message for one finished command from the
// variables the hook set. cwd is the emitter's working directory, which it
// inherits from the shell. Invalid command names are dropped rather than
// failing the whole message, so the command is still counted.
func EmitFromEnv(getenv func(string) string, cwd string, now time.Time) (wire.Message, error) {
	start, end, err := commandTimes(getenv, now)
	if err != nil {
		return wire.Message{}, err
	}
	exit, err := strconv.Atoi(getenv(EnvExit))
	if err != nil {
		return wire.Message{}, fmt.Errorf("shell: %s: %w", EnvExit, err)
	}
	pid, _ := strconv.Atoi(getenv(EnvShellPID))

	cmd := &wire.ShellCommand{
		Argv0:    getenv(EnvArgv0),
		Subcmd:   getenv(EnvSubcmd),
		Exit:     exit,
		StartMs:  start,
		EndMs:    max(end, start),
		Cwd:      cwd,
		TTY:      getenv(EnvTTY),
		ShellPID: pid,
	}
	m := wire.Message{V: wire.Version, Kind: wire.KindShellCommand, SentMs: now.UnixMilli(), Shell: cmd}
	if cmd.Argv0 == "" {
		cmd.Subcmd = ""
	}
	// The names are the only fields that can carry user text. Drop whichever
	// the daemon would reject, subcommand first, so the command still counts.
	if validate(m) != nil {
		cmd.Subcmd = ""
	}
	if validate(m) != nil {
		cmd.Argv0 = ""
	}
	if err := validate(m); err != nil {
		return wire.Message{}, err
	}
	return m, nil
}

// commandTimes reads the command's start and end: EnvStart and EnvEnd
// when the shell has a clock, else EnvDuration back from now.
func commandTimes(getenv func(string) string, now time.Time) (start, end int64, err error) {
	if getenv(EnvStart) == "" {
		dur, err := strconv.ParseInt(getenv(EnvDuration), 10, 64)
		if err != nil || dur < 0 {
			return 0, 0, fmt.Errorf("shell: %s: not a duration", EnvDuration)
		}
		end = now.UnixMilli()
		return end - dur, end, nil
	}
	if start, err = epochMillis(getenv(EnvStart)); err != nil {
		return 0, 0, fmt.Errorf("shell: %s: %w", EnvStart, err)
	}
	if end, err = epochMillis(getenv(EnvEnd)); err != nil {
		return 0, 0, fmt.Errorf("shell: %s: %w", EnvEnd, err)
	}
	return start, end, nil
}

// validate applies the daemon's own rules, so a message the daemon would
// reject is fixed or dropped here instead.
func validate(m wire.Message) error {
	b, err := wire.Encode(m)
	if err != nil {
		return err
	}
	_, err = wire.Decode(strings.NewReader(string(b)))
	return err
}

// epochMillis parses zsh's $EPOCHREALTIME ("1729512345.123456789") into
// Unix milliseconds without going through float64.
func epochMillis(s string) (int64, error) {
	sec, frac, _ := strings.Cut(strings.Replace(s, ",", ".", 1), ".")
	secs, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || secs <= 0 {
		return 0, errors.New("not an epoch timestamp")
	}
	frac = (frac + "000")[:3]
	ms, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errors.New("not an epoch timestamp")
	}
	return secs*1000 + ms, nil
}
