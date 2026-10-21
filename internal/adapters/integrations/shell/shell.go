// Package shell installs the interactive-shell hooks and turns what they
// emit into wire messages. Phase 0 supports zsh.
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

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/fileedit"

	"github.com/kshitijdhara/burnout-tracker/internal/wire"
)

//go:embed hooks/burnout.zsh
var zshScript string

const (
	binPlaceholder     = "__BURNOUT_BIN__"
	subcmdsPlaceholder = "__BURNOUT_SUBCMDS__"
)

// Environment variables the hook sets for `burnout emit shell`.
const (
	EnvArgv0    = "BURNOUT_ARGV0"
	EnvSubcmd   = "BURNOUT_SUBCMD"
	EnvExit     = "BURNOUT_EXIT"
	EnvStart    = "BURNOUT_T0"
	EnvEnd      = "BURNOUT_T1"
	EnvTTY      = "BURNOUT_TTY"
	EnvShellPID = "BURNOUT_SHELL_PID"
)

// RenderZsh returns the zsh hook script with binPath and the subcommand
// allowlist substituted.
func RenderZsh(binPath string) string {
	s := strings.Replace(zshScript, binPlaceholder, fileedit.ShellQuote(binPath), 1)
	return strings.Replace(s, subcmdsPlaceholder, zshSubcommandTable(), 1)
}

// EmitFromEnv builds the wire message for one finished command from the
// variables the hook set. cwd is the emitter's working directory, which it
// inherits from the shell. Invalid command names are dropped rather than
// failing the whole message, so the command is still counted.
func EmitFromEnv(getenv func(string) string, cwd string, now time.Time) (wire.Message, error) {
	start, err := epochMillis(getenv(EnvStart))
	if err != nil {
		return wire.Message{}, fmt.Errorf("shell: %s: %w", EnvStart, err)
	}
	end, err := epochMillis(getenv(EnvEnd))
	if err != nil {
		return wire.Message{}, fmt.Errorf("shell: %s: %w", EnvEnd, err)
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
