package service

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf16"

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

// taskReserved are the characters a scheduled task's command line gives
// meaning to: quoting, and %VAR% expansion in Command and Arguments.
const taskReserved = "\"%"

// RenderTask returns the Task Scheduler definition, UTF-16LE with a BOM as
// schtasks /Create /XML expects: run at the given user's logon, in their
// interactive session only (the tray must be able to show), restarted a
// minute after a failed run (the shortest interval Task Scheduler allows)
// up to 999 times, with no time limit and on battery too. Running it again
// replaces the running daemon, so a reinstall starts the new binary. A start the
// daemon declines exits 0, a success, and isn't retried. user is the
// account's SID.
func RenderTask(bin string, args []string, user string) ([]byte, error) {
	if !winAbs(bin) {
		return nil, fmt.Errorf("service: binary path %q is not absolute", bin)
	}
	if err := checkValue("binary path", bin, taskReserved); err != nil {
		return nil, err
	}
	for _, a := range args {
		if err := checkValue("argument", a, taskReserved+" "); err != nil {
			return nil, err
		}
	}
	if !validSID(user) {
		return nil, fmt.Errorf("service: %q is not a user SID", user)
	}
	esc := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	def := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>burnout-tracker daemon (local only; nothing leaves this device)</Description>
    <URI>\` + TaskName(user) + `</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + user + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + user + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>StopExisting</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(bin) + `</Command>
      <Arguments>` + esc(strings.Join(args, " ")) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
	def = strings.ReplaceAll(def, "\n", "\r\n")
	u := utf16.Encode([]rune(def))
	out := make([]byte, 2, 2+2*len(u))
	out[0], out[1] = 0xff, 0xfe // byte order mark, little-endian
	for _, c := range u {
		out = binary.LittleEndian.AppendUint16(out, c)
	}
	return out, nil
}

// TaskName is the scheduled task's name for the user with this SID. Task
// names are machine-wide, so each user gets their own.
func TaskName(sid string) string { return "burnout-tracker-" + sid }

// winAbs reports a Windows absolute path, C:\… or \\server\share\…, on any
// OS, so the renderer is tested everywhere.
func winAbs(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return len(p) > 2
	}
	return len(p) >= 3 && p[1] == ':' && p[2] == '\\' &&
		('A' <= p[0] && p[0] <= 'Z' || 'a' <= p[0] && p[0] <= 'z')
}

// validSID reports a SID in its string form, S-1-5-21-…: it goes into the
// task's name and XML unescaped.
func validSID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) < 3 || parts[0] != "S" {
		return false
	}
	for _, p := range parts[1:] {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
}
