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
		{"camera on", mediaState{OK: true, PerProcessMic: true, CameraOn: true}, true, nil},
		{"device fallback before macOS 14.2", mediaState{OK: true, MicAny: true}, true, nil},
		{"device state ignored when per-app is known", mediaState{OK: true, PerProcessMic: true, MicAny: true}, false, nil},
		{"media not read", mediaState{MicPIDs: []int{300}, CameraOn: true}, false, nil},
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
