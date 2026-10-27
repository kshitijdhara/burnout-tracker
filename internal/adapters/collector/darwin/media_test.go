package darwin

import (
	"slices"
	"strings"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// meetingState: an editor in front on monitor 0, a browser (pid 300) with
// two windows on monitor 1, and a call app (pid 400) fully covered by the
// editor.
func meetingState(m mediaState) rawState {
	return rawState{
		Windows: []rawWindow{
			{Number: 10, PID: 100, Layer: 0, Bounds: rect(0, 0, 1000, 800), Owner: "Code"},
			{Number: 30, PID: 300, Layer: 0, Bounds: rect(1000, 0, 1000, 400), Owner: "Google Chrome"},
			{Number: 31, PID: 300, Layer: 0, Bounds: rect(1000, 400, 1000, 400), Owner: "Google Chrome"},
			{Number: 40, PID: 400, Layer: 0, Bounds: rect(100, 100, 400, 300), Owner: "zoom.us"},
		},
		Displays: []model.Rect{rect(0, 0, 1000, 800), rect(1000, 0, 1000, 800)},
		Media:    m,
	}
}

func marked(snap model.Snapshot) (calls, video []model.WindowID) {
	for _, w := range snap.Windows {
		if w.InCall {
			calls = append(calls, w.ID)
		}
		if w.HasVideo {
			video = append(video, w.ID)
		}
	}
	return calls, video
}

func TestMediaCalls(t *testing.T) {
	cases := []struct {
		name       string
		media      mediaState
		wantActive bool
		wantCalls  []model.WindowID
	}{
		{"browser call goes to its frontmost window", mediaState{OK: true, PerProcessMic: true, MicPIDs: []int{300}}, true, []model.WindowID{30}},
		{"covered call app still holds presence", mediaState{OK: true, PerProcessMic: true, MicPIDs: []int{400}}, true, nil},
		{"call app on another Space", mediaState{OK: true, PerProcessMic: true, MicPIDs: []int{999}}, true, nil},
		{"no app capturing audio", mediaState{OK: true, PerProcessMic: true}, false, nil},
		{"device fallback before macOS 14.2", mediaState{OK: true, MicAny: true}, true, nil},
		{"device state ignored when per-app is known", mediaState{OK: true, PerProcessMic: true, MicAny: true}, false, nil},
		{"media not read", mediaState{MicPIDs: []int{300}}, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := newTranslator(bundles(nil)).snapshot(at, meetingState(tc.media), inputCounts{})
			calls, _ := marked(snap)
			if snap.CallActive != tc.wantActive || !slices.Equal(calls, tc.wantCalls) {
				t.Fatalf("CallActive=%v calls=%v, want %v %v", snap.CallActive, calls, tc.wantActive, tc.wantCalls)
			}
		})
	}
}

func TestMediaVideo(t *testing.T) {
	m := mediaState{OK: true, PerProcessMic: true, DisplayAwakePIDs: []int{300, 777}} // 777: caffeinate, no windows
	snap := newTranslator(bundles(nil)).snapshot(at, meetingState(m), inputCounts{})
	if _, video := marked(snap); !slices.Equal(video, []model.WindowID{30}) {
		t.Fatalf("video windows = %v, want the browser's frontmost", video)
	}
	if snap.CallActive {
		t.Fatal("video alone is not a call")
	}
}

func TestMediaLimitations(t *testing.T) {
	if got := mediaLimitations(mediaState{OK: true, PerProcessMic: true, Responsible: true}); len(got) != 0 {
		t.Fatalf("full support reported limitations: %v", got)
	}
	got := strings.Join(mediaLimitations(mediaState{OK: true}), "; ")
	if !strings.Contains(got, "14.2") || !strings.Contains(got, "browser") {
		t.Fatalf("limitations = %q", got)
	}
}

// fakeProcs models: Chrome (300) with an audio helper (301) and a GPU
// helper (302); WebKit's GPU service (501) working for Safari (500);
// caffeinate (900) run from Terminal (100); a voice-trigger agent (700).
type fakeProcs struct{}

func (fakeProcs) responsible(pid int) int {
	if app, ok := map[int]int{301: 300, 302: 300, 501: 500, 900: 100}[pid]; ok {
		return app
	}
	return pid
}
func (fakeProcs) regularApp(pid int) bool { return pid == 300 || pid == 500 || pid == 100 }
func (fakeProcs) executable(pid int) string {
	return map[int]string{
		300: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		302: "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Helpers/Google Chrome Helper (GPU).app/Contents/MacOS/Google Chrome Helper (GPU)",
		501: "/System/Library/Frameworks/WebKit.framework/Versions/A/XPCServices/com.apple.WebKit.GPU.xpc/Contents/MacOS/com.apple.WebKit.GPU",
		900: "/usr/bin/caffeinate",
	}[pid]
}

func TestResolveMedia(t *testing.T) {
	raw := rawMedia{
		PerProcessMic: true, Responsible: true,
		MicPIDs:   []int{301, 700, 300}, // Chrome's audio helper, a voice trigger, Chrome again
		AwakePIDs: []int{302, 501, 900}, // Chrome's GPU helper, WebKit for Safari, caffeinate -d
	}
	st := resolveMedia(raw, fakeProcs{})
	if !st.OK || !slices.Equal(st.MicPIDs, []int{300}) {
		t.Errorf("calls = %v, want Chrome once and no background agent", st.MicPIDs)
	}
	if !slices.Equal(st.DisplayAwakePIDs, []int{300, 500}) {
		t.Errorf("video = %v, want Chrome and Safari but not the terminal running caffeinate", st.DisplayAwakePIDs)
	}
}

func TestInAppBundle(t *testing.T) {
	for path, want := range map[string]bool{
		"/Applications/zoom.us.app/Contents/MacOS/zoom.us": true,
		"/System/Library/Frameworks/WebKit.framework/Versions/A/XPCServices/com.apple.WebKit.GPU.xpc/Contents/MacOS/com.apple.WebKit.GPU": true,
		"/usr/bin/caffeinate":         false,
		"/opt/homebrew/bin/something": false,
		"":                            false,
	} {
		if inAppBundle(path) != want {
			t.Errorf("inAppBundle(%q) = %v", path, !want)
		}
	}
}

// macOS doesn't say which of an app's windows plays the video: the most
// visible one gets it, so a doc in front doesn't take a video's credit
// from a bigger window behind it.
func TestMediaGoesToMostVisibleWindow(t *testing.T) {
	ws := []model.Window{
		{ID: 1, PID: 300, Visible: 0.3}, // frontmost but mostly off-screen
		{ID: 2, PID: 300, Visible: 0.9},
		{ID: 3, PID: 400, Visible: 1},
	}
	if w := mostVisibleOf(ws, 300); w == nil || w.ID != 2 {
		t.Fatalf("got %+v, want window 2", w)
	}
	tie := []model.Window{{ID: 1, PID: 300, Visible: 1}, {ID: 2, PID: 300, Visible: 1}}
	if w := mostVisibleOf(tie, 300); w == nil || w.ID != 1 {
		t.Fatalf("tie: got %+v, want the frontmost", w)
	}
	if mostVisibleOf(ws, 999) != nil {
		t.Fatal("unknown pid matched a window")
	}
}

func TestDisplayAsleepIsNotLocked(t *testing.T) {
	st := meetingState(mediaState{})
	st.DisplayAsleep = true
	snap := newTranslator(bundles(nil)).snapshot(at, st, inputCounts{})
	if !snap.DisplayAsleep || snap.Locked {
		t.Fatalf("DisplayAsleep=%v Locked=%v", snap.DisplayAsleep, snap.Locked)
	}
}

// Another app chooses its own name: escape sequences or bidi overrides in it
// must not reach anything we store or print.
func TestCleanLabelDropsControls(t *testing.T) {
	for in, want := range map[string]string{
		"Evil\x1b[2J App\x07": "Evil[2J App",
		"abc\u202eexe":        "abcexe",
		"  Café  ":            "Café",
	} {
		if got := cleanLabel(in); got != want {
			t.Errorf("cleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
