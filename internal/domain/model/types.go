package model

import "time"

// Category is a role-aware activity label. Stored in clear text.
type Category string

// Categories shared by every role profile. Role taxonomies add more.
const (
	CategoryPrivate       Category = "private" // excluded or sensitive: time kept, identity dropped
	CategoryUncategorized Category = "uncategorized"
	CategoryCode          Category = "code"
	CategoryTerminal      Category = "terminal"
	CategoryBrowser       Category = "browser"
	CategoryComms         Category = "comms"
	CategoryMeeting       Category = "meeting"
	CategoryDocs          Category = "docs"
	CategoryMedia         Category = "media"
	CategoryRemoteDesktop Category = "remote-desktop"
	CategoryOther         Category = "other"
)

// WindowID identifies a window for the lifetime of one daemon run. It is an
// opaque OS handle and is never persisted; persistence uses a keyed HMAC.
type WindowID uint64

// Rect is a window or monitor rectangle in global screen coordinates.
type Rect struct{ X, Y, W, H int }

// Area returns the rectangle's area, or zero for empty rectangles.
func (r Rect) Area() int {
	if r.W <= 0 || r.H <= 0 {
		return 0
	}
	return r.W * r.H
}

// Window is one visible window as seen in a single collector snapshot.
type Window struct {
	ID       WindowID
	AppID    string // bundle id or executable name
	PID      int    // owning process; used to map terminals to shells and agents
	Bounds   Rect
	Monitor  int
	Visible  float64 // unoccluded fraction of the window on an active monitor, 0..1
	Focused  bool
	HasVideo bool              // video playing in this window
	InCall   bool              // an active call (microphone in use) belongs to this window
	Title    Sensitive[string] // empty unless title classification is enabled; never persisted
}

// Snapshot is everything the attention engine needs for one tick.
type Snapshot struct {
	At            time.Time
	Windows       []Window
	Monitors      []Rect   // active displays in global coordinates, for topology
	PointerWindow WindowID // window under the pointer, 0 if none
	PointerMoved  bool
	IdleFor       time.Duration // time since last user input of any kind
	Keys          int           // key presses since the previous snapshot (opt-in; 0 when off)
	Scrolls       int           // scroll/click events since the previous snapshot (opt-in; 0 when off)
	Locked        bool          // screen locked, display asleep or session inactive
	// CallActive is true while an app the user runs (not a background
	// agent) is using the microphone, or a camera is on. It holds presence
	// even when the call's window is hidden; Window.InCall marks the call's
	// window when it is in the snapshot.
	CallActive    bool
	InputCountsOn bool // whether Keys/Scrolls are real counts
}
