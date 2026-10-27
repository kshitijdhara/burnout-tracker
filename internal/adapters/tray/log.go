package tray

import (
	"log"
	"log/slog"
	"strings"
)

// logTo sends the standard logger, which the systray package reports its
// errors to, into the daemon's log.
func logTo(l *slog.Logger) {
	log.SetFlags(0)
	log.SetOutput(logWriter{l})
}

type logWriter struct{ log *slog.Logger }

func (w logWriter) Write(p []byte) (int, error) {
	w.log.Warn("tray", "msg", strings.TrimSpace(string(p)))
	return len(p), nil
}
