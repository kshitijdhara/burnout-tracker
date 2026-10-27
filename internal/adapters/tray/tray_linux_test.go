package tray

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// sessionBus starts a private D-Bus session bus for the test and points
// the process at it.
func sessionBus(t *testing.T) {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon isn't installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address",
		"--address=unix:path="+filepath.Join(t.TempDir(), "bus"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))
}

// panel is a fake desktop panel: a StatusNotifierWatcher with a host.
type panel struct {
	conn *dbus.Conn
	mu   sync.Mutex
	item string // the bus name of the registered icon
}

func (p *panel) RegisterStatusNotifierItem(sender dbus.Sender, _ string) *dbus.Error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.item = string(sender)
	return nil
}

func (p *panel) registered() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.item
}

func startPanel(t *testing.T) *panel {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &panel{conn: conn}
	const path = "/StatusNotifierWatcher"
	if err := conn.Export(p, path, watcher); err != nil {
		t.Fatal(err)
	}
	if _, err := prop.Export(conn, path, prop.Map{watcher: {
		"IsStatusNotifierHostRegistered": {Value: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(watcher, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v %v", reply, err)
	}
	return p
}

// menuObject is the icon's menu, found as a panel finds it: from the
// item's Menu property. Nil until the icon has registered.
func (p *panel) menuObject() dbus.BusObject {
	name := p.registered()
	if name == "" {
		return nil
	}
	v, err := p.conn.Object(name, "/StatusNotifierItem").GetProperty("org.kde.StatusNotifierItem.Menu")
	if err != nil {
		return nil
	}
	path, ok := v.Value().(dbus.ObjectPath)
	if !ok {
		return nil
	}
	return p.conn.Object(name, path)
}

// entry is one menu item as the panel sees it.
type entry struct {
	id               int32
	label            string
	visible, enabled bool
}

func (p *panel) menu(t *testing.T) []entry {
	t.Helper()
	var rev uint32
	var root struct {
		ID       int32
		Props    map[string]dbus.Variant
		Children []dbus.Variant
	}
	obj := p.menuObject()
	if obj == nil {
		return nil
	}
	if err := obj.Call("com.canonical.dbusmenu.GetLayout", 0, int32(0), int32(-1), []string{}).Store(&rev, &root); err != nil {
		return nil // not exported yet
	}
	var out []entry
	for _, c := range root.Children {
		f, ok := c.Value().([]any)
		if !ok || len(f) < 2 {
			t.Fatalf("menu item %v", c)
		}
		props, _ := f[1].(map[string]dbus.Variant)
		e := entry{id: f[0].(int32), visible: true, enabled: true}
		if v, ok := props["label"]; ok {
			e.label, _ = v.Value().(string)
		}
		if v, ok := props["visible"]; ok {
			e.visible, _ = v.Value().(bool)
		}
		if v, ok := props["enabled"]; ok {
			e.enabled, _ = v.Value().(bool)
		}
		if e.label != "" {
			out = append(out, e)
		}
	}
	return out
}

// lookup finds a visible or hidden item by its label.
func (p *panel) lookup(t *testing.T, label string) (entry, bool) {
	t.Helper()
	for _, e := range p.menu(t) {
		if e.label == label {
			return e, true
		}
	}
	return entry{}, false
}

func (p *panel) find(t *testing.T, label string) entry {
	t.Helper()
	e, ok := p.lookup(t, label)
	if !ok {
		t.Fatalf("no %q in the menu: %+v", label, p.menu(t))
	}
	return e
}

// shows reports whether a visible item has the label.
func (p *panel) shows(t *testing.T, label string) bool {
	t.Helper()
	e, ok := p.lookup(t, label)
	return ok && e.visible
}

func (p *panel) click(t *testing.T, label string) {
	t.Helper()
	e := p.find(t, label)
	if err := p.menuObject().Call("com.canonical.dbusmenu.Event", 0, e.id, "clicked", dbus.MakeVariant(""), uint32(0)).Err; err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// fakeControls records what the menu asked for.
type fakeControls struct {
	mu     sync.Mutex
	status Status
	calls  []string
	fail   bool
}

func (f *fakeControls) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.fail {
		return errors.New("no answer")
	}
	return nil
}

func (f *fakeControls) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeControls) set(s Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = s
}

func (f *fakeControls) menu() Menu {
	names := map[PauseFor]string{PauseHour: "pause 1h", PauseTomorrow: "pause tomorrow", PauseUntilResumed: "pause"}
	ranges := map[Range]string{LastHour: "delete hour", Today: "delete today"}
	return Menu{
		Status: func() Status {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.status
		},
		Pause:         func(p PauseFor) error { return f.record(names[p]) },
		Resume:        func() error { return f.record("resume") },
		Delete:        func(r Range) error { return f.record(ranges[r]) },
		OpenDashboard: func() error { return f.record("dashboard") },
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestNoHost(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "none"))
	if why := noHost(context.Background()); !strings.Contains(why, "session bus") {
		t.Errorf("no bus: %q", why)
	}
	sessionBus(t)
	if why := noHost(context.Background()); !strings.Contains(why, "no tray") {
		t.Errorf("no panel: %q", why)
	}
}

// TestTrayOnLinux runs the real tray against a fake panel: the icon
// registers before work starts, and the menu's items do what they say.
func TestTrayOnLinux(t *testing.T) {
	sessionBus(t)
	p := startPanel(t)
	f := &fakeControls{status: Status{Text: "Recording"}}

	err := Run(context.Background(), f.menu(), func(_ context.Context, shown bool) error {
		if !shown {
			t.Error("work started without the icon")
			return nil
		}
		eventually(t, "the icon to register", func() bool { return p.registered() != "" })
		eventually(t, "the menu", func() bool { return len(p.menu(t)) > 0 })

		want := []string{"Recording", "Pause for an hour", "Pause until tomorrow", "Pause until I resume",
			"Delete the last hour", "Delete today", "Open dashboard"}
		var got []string
		for _, e := range p.menu(t) {
			if e.visible {
				got = append(got, e.label)
			}
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("menu:\n got %q\nwant %q", got, want)
		}
		if p.find(t, "Recording").enabled {
			t.Error("the status line is clickable")
		}
		for _, h := range []string{"Resume", "Quit", "Hide"} {
			if p.shows(t, h) {
				t.Errorf("%q shows while recording", h)
			}
		}

		p.click(t, "Pause for an hour")
		eventually(t, "the pause", func() bool { return len(f.called()) == 1 })
		f.set(Status{Paused: true, Text: "Paused until 15:30"})
		eventually(t, "the paused menu", func() bool {
			return p.shows(t, "Resume") && !p.shows(t, "Pause for an hour") && p.shows(t, "Paused until 15:30")
		})
		p.click(t, "Resume")
		eventually(t, "the resume", func() bool { return len(f.called()) == 2 })
		f.set(Status{Text: "Recording"})

		eventually(t, "the menu to settle", func() bool { return p.find(t, "Delete today").enabled })
		p.click(t, "Delete today")
		eventually(t, "the delete note", func() bool { return p.shows(t, "Deleted today") })

		f.mu.Lock()
		f.fail = true
		f.mu.Unlock()
		eventually(t, "the menu to settle", func() bool { return p.find(t, "Delete the last hour").enabled })
		p.click(t, "Delete the last hour")
		eventually(t, "the failure note", func() bool { return p.shows(t, "Couldn't delete the last hour") })

		if got, want := strings.Join(f.called(), ","), "pause 1h,resume,delete today,delete hour"; got != want {
			t.Errorf("calls %s, want %s", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
