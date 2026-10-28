// Package wayland is the GNOME Wayland desktop collector. Wayland gives no
// client a view of other clients' windows, so a GNOME Shell extension
// (extensions/gnome-shell) reads them inside the compositor and serves them
// over a private, owner-only Unix socket (ADR-0010). The collector asks for
// a snapshot each tick, works out visibility and the window under the
// pointer itself, and reads idle time from Mutter and lock state from
// logind.
//
// This file is the wire contract, schemas/v1/gnome-bridge.schema.json:
// newline-delimited JSON, one reply per request. The pure decoding and
// translation are tested on every platform; the socket and D-Bus calls
// live in *_linux.go.
package wayland

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// UUID is the extension's uuid: its metadata.json, its install directory
// and the name `gnome-extensions enable` takes.
const UUID = "burntout-bridge@kshitijdhara.github.io"

// Version is the protocol version this agent speaks. MinVersion is the
// oldest extension it still reads: an upgraded extension doesn't load into
// a running GNOME Shell until the user logs out and back in, so agent N
// meets extension N-1 after every upgrade.
const (
	Version    = 1
	MinVersion = max(1, Version-1)
)

// Socket and token file names, inside paths.RuntimeDir().
const (
	SocketName = "gnome-bridge.sock"
	TokenName  = "gnome-bridge.token"
)

// Limits on a reply, kept in step with the schema and the extension.
const (
	MaxReplyBytes = 1 << 20
	MaxWindows    = 512
	MaxMonitors   = 32
	MaxClassBytes = 1024 // 256 characters in the extension
	MaxTitleBytes = 4096 // 1024 UTF-16 units in the extension
	MaxErrorBytes = 256
	MaxCoord      = 1 << 20
	MaxPID        = 1 << 22 // Linux pid_max
)

// Window types the extension reports. Desktop and dock windows cover what
// is beneath them but are not reported; "other" (menus, tooltips,
// override-redirect windows) takes no part.
const (
	typeNormal  = "normal"
	typeDialog  = "dialog"
	typeUtility = "utility"
	typeDesktop = "desktop"
	typeDock    = "dock"
	typeOther   = "other"
)

var windowTypes = []string{typeNormal, typeDialog, typeUtility, typeDesktop, typeDock, typeOther}

// request is what the agent sends. The first request on a connection is a
// hello carrying the token; titles is fixed for the connection's life.
type request struct {
	V      int    `json:"v"`
	Op     string `json:"op"`
	Token  string `json:"token,omitempty"`
	Titles bool   `json:"titles,omitempty"`
}

// reply is what the extension sends back. Unknown fields are ignored, so
// a newer extension may add fields without a version bump.
type reply struct {
	V     int    `json:"v"`
	Op    string `json:"op"`
	Error string `json:"error,omitempty"`

	// hello
	Extension string `json:"extension,omitempty"`
	Shell     string `json:"shell,omitempty"`

	// snapshot
	Windows  []window `json:"windows,omitempty"`
	Monitors []rect   `json:"monitors,omitempty"`
	Focus    uint64   `json:"focus,omitempty"`
	Pointer  *point   `json:"pointer,omitempty"`
}

// window is one window in stacking order, bottom first.
type window struct {
	ID          uint64  `json:"id"`
	PID         int     `json:"pid"`
	WMClass     string  `json:"wm_class"`
	App         string  `json:"app"`
	Type        string  `json:"type"`
	Minimised   bool    `json:"minimised"`
	OnWorkspace bool    `json:"on_workspace"`
	Frame       rect    `json:"frame"`
	Title       *string `json:"title,omitempty"`
}

type rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// VersionError is a reply in a protocol version this agent can't read.
type VersionError struct{ Got int }

func (e *VersionError) Error() string {
	if e.Got > Version {
		return fmt.Sprintf("gnome bridge: extension speaks protocol v%d, newer than this agent (v%d); update burntout", e.Got, Version)
	}
	return fmt.Sprintf("gnome bridge: extension speaks protocol v%d, older than this agent reads (v%d–v%d); log out and back in to load the updated extension", e.Got, MinVersion, Version)
}

// RefusedError is an error reply: the extension turned a request down.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "gnome bridge refused: " + e.Reason }

var errMalformed = errors.New("gnome bridge: malformed reply")

// decodeReply parses and checks one reply line (without its newline).
func decodeReply(line []byte) (reply, error) {
	if len(line) > MaxReplyBytes {
		return reply{}, fmt.Errorf("%w: %d bytes", errMalformed, len(line))
	}
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		return reply{}, fmt.Errorf("%w: %w", errMalformed, err)
	}
	if r.V < MinVersion || r.V > Version {
		return reply{}, &VersionError{Got: r.V}
	}
	switch r.Op {
	case "hello":
		if !utf8.ValidString(r.Extension) || !utf8.ValidString(r.Shell) || len(r.Extension) > 64 || len(r.Shell) > 64 {
			return reply{}, fmt.Errorf("%w: hello versions", errMalformed)
		}
	case "error":
		if len(r.Error) > MaxErrorBytes || !utf8.ValidString(r.Error) {
			return reply{}, fmt.Errorf("%w: error reason", errMalformed)
		}
	case "snapshot":
		if err := checkSnapshot(r); err != nil {
			return reply{}, fmt.Errorf("%w: %w", errMalformed, err)
		}
	default:
		return reply{}, fmt.Errorf("%w: op %.32q", errMalformed, r.Op)
	}
	return r, nil
}

func checkSnapshot(r reply) error {
	if r.Pointer == nil {
		return errors.New("no pointer")
	}
	if len(r.Windows) > MaxWindows || len(r.Monitors) > MaxMonitors {
		return fmt.Errorf("%d windows, %d monitors", len(r.Windows), len(r.Monitors))
	}
	if !inRange(r.Pointer.X) || !inRange(r.Pointer.Y) {
		return errors.New("pointer out of range")
	}
	for _, m := range r.Monitors {
		if !validRect(m) {
			return errors.New("monitor out of range")
		}
	}
	for _, w := range r.Windows {
		switch {
		case !validRect(w.Frame):
			return errors.New("window frame out of range")
		case w.PID < 0 || w.PID > MaxPID:
			return errors.New("window pid out of range")
		case !validString(w.WMClass, MaxClassBytes) || !validString(w.App, MaxClassBytes):
			return errors.New("window class too long or not UTF-8")
		case w.Title != nil && !validString(*w.Title, MaxTitleBytes):
			return errors.New("window title too long or not UTF-8")
		case !slices.Contains(windowTypes, w.Type):
			return fmt.Errorf("window type %.32q", w.Type)
		}
	}
	return nil
}

func inRange(v int) bool { return v >= -MaxCoord && v <= MaxCoord }

func validRect(r rect) bool {
	return inRange(r.X) && inRange(r.Y) && r.W >= 0 && r.H >= 0 && r.W <= MaxCoord && r.H <= MaxCoord
}

func validString(s string, maxBytes int) bool {
	return len(s) <= maxBytes && utf8.ValidString(s)
}
