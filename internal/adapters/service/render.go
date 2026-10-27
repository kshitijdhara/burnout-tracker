package service

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
	"strings"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

// systemdReserved are the characters systemd's unit quoting, specifiers and
// variable expansion give meaning to; paths and values may not contain them.
const systemdReserved = "\"'\\%$`"

// RenderPlist returns the LaunchAgent: run at login, restart after a crash
// (not after a clean exit), at most every 30 s, only in a GUI session, with
// the given environment. Crash output (a Go panic, a failed start) goes to
// logDir; the daemon's own log goes to its rotating file.
func RenderPlist(bin string, args []string, env map[string]string, logDir string) ([]byte, error) {
	if err := checkPath("binary path", bin, ""); err != nil {
		return nil, err
	}
	if err := checkPath("log folder", logDir, ""); err != nil {
		return nil, err
	}
	for _, a := range args {
		if err := checkValue("argument", a, ""); err != nil {
			return nil, err
		}
	}
	for k, v := range env {
		if err := checkValue("environment", k+"="+v, ""); err != nil {
			return nil, err
		}
	}
	esc := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(bin) + `</string>
`)
	for _, a := range args {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	if len(env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range sortedKeys(env) {
			b.WriteString("\t\t<key>" + esc(k) + "</key>\n\t\t<string>" + esc(env[k]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString(`	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>StandardOutPath</key>
	<string>/dev/null</string>
	<key>StandardErrorPath</key>
	<string>` + esc(filepath.Join(logDir, paths.CrashLogFile)) + `</string>
</dict>
</plist>
`)
	return b.Bytes(), nil
}

// RenderUnit returns the systemd user unit: started with the user session,
// restarted 30 s after a crash with no start limit, like launchd's
// ThrottleInterval, or after a minute without a watchdog ping from the
// daemon's loop (a hang). A start the daemon declines (no consent, say)
// exits 0 and isn't retried.
func RenderUnit(bin string, args []string, env map[string]string) ([]byte, error) {
	if err := checkPath("binary path", bin, systemdReserved); err != nil {
		return nil, err
	}
	for _, a := range args {
		if err := checkValue("argument", a, systemdReserved+" "); err != nil {
			return nil, err
		}
	}
	var envLines strings.Builder
	for _, k := range sortedKeys(env) {
		if err := checkValue("environment", k+"="+env[k], systemdReserved); err != nil {
			return nil, err
		}
		envLines.WriteString(`Environment="` + k + "=" + env[k] + "\"\n")
	}
	return []byte(`[Unit]
Description=burnout-tracker daemon (local only; nothing leaves this device)
StartLimitIntervalSec=0

[Service]
Type=notify
` + envLines.String() + `ExecStart="` + bin + `" ` + strings.Join(args, " ") + `
Restart=on-failure
RestartSec=30
WatchdogSec=60

[Install]
WantedBy=default.target
`), nil
}
