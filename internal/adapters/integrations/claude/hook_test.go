package claude

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/procinfo"

	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/wire"
)

const canary = "CANARY-7f3a9c"

func payloadJSON(t *testing.T, extra map[string]any) string {
	t.Helper()
	p := map[string]any{
		"session_id":      "0b9c1f2e-1111-4222-8333-944455556666",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"cwd":             "/home/u/" + canary + "/proj",
		"transcript_path": "/home/u/.claude/projects/" + canary + ".jsonl",
		"prompt":          "please use token " + canary,
		"tool_input":      map[string]any{"command": "curl -H 'Authorization: " + canary + "'", "nested": []any{canary, 1, true, nil}},
		"tool_response":   map[string]any{"stdout": canary},
	}
	for k, v := range extra {
		p[k] = v
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type fakeProcs struct {
	ancestors []int
	names     map[int]string
}

func (f fakeProcs) Lineage(int, int) ([]procinfo.Proc, error) {
	out := make([]procinfo.Proc, len(f.ancestors))
	for i, pid := range f.ancestors {
		out[i] = procinfo.Proc{PID: pid, Name: f.names[pid]}
	}
	return out, nil
}

func TestParsePayloadAllowlist(t *testing.T) {
	p, err := ParsePayload(strings.NewReader(payloadJSON(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if p.Event != "PreToolUse" || p.ToolName != "Bash" || p.SessionID == "" {
		t.Fatalf("got %+v", p)
	}
	if p.Cwd.Reveal() != "/home/u/"+canary+"/proj" {
		t.Fatal("cwd not kept")
	}
}

// The prompt, tool input/output and transcript path must never reach the
// message sent to the daemon. cwd is the one field that crosses the
// same-user socket before the daemon replaces it with an HMAC.
func TestRunNeverForwardsContent(t *testing.T) {
	var sent []wire.Message
	send := func(m wire.Message) error { sent = append(sent, m); return nil }
	in := payloadJSON(t, map[string]any{"cwd": "/home/u/proj"})
	procs := fakeProcs{ancestors: []int{11, 22, 33}, names: map[int]string{11: "sh", 22: "claude", 33: "zsh"}}
	Run(strings.NewReader(in), func() time.Time { return time.UnixMilli(1792542700000) }, send, procs)

	if len(sent) != 1 {
		t.Fatalf("sent %d messages", len(sent))
	}
	b, err := json.Marshal(sent[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), canary) {
		t.Fatalf("content leaked into the message: %s", b)
	}
	a := sent[0].Agent
	if a.Agent != "claude" || a.Event != "PreToolUse" || a.ToolName != "Bash" || a.AgentPID != 22 || a.AtMs != 1792542700000 {
		t.Fatalf("got %+v", a)
	}
	// The daemon must accept what the hook sends.
	if _, err := wire.Decode(strings.NewReader(string(b))); err != nil {
		t.Fatalf("daemon would reject the message: %v", err)
	}
}

func TestRunFallsBackToParentPID(t *testing.T) {
	var got int
	send := func(m wire.Message) error { got = m.Agent.AgentPID; return nil }
	Run(strings.NewReader(payloadJSON(t, nil)), time.Now, send, fakeProcs{ancestors: []int{5}, names: map[int]string{5: "node"}})
	if got != os.Getppid() {
		t.Fatalf("pid = %d, want parent %d", got, os.Getppid())
	}
}

func TestRunDropsUnknownAndBadInput(t *testing.T) {
	inputs := []string{
		payloadJSON(t, map[string]any{"hook_event_name": "SomethingNew"}),
		"not json",
		`["array"]`,
		"",
		`{"hook_event_name": "Stop"`, // truncated
	}
	for _, in := range inputs {
		Run(strings.NewReader(in), time.Now, func(wire.Message) error {
			t.Fatalf("sent a message for %q", in)
			return nil
		}, nil)
	}
}

func TestRunSurvivesSendErrorsAndPanics(t *testing.T) {
	Run(strings.NewReader(payloadJSON(t, nil)), time.Now, func(wire.Message) error { return errors.New("down") }, nil)
	Run(strings.NewReader(payloadJSON(t, nil)), time.Now, func(wire.Message) error { panic("boom") }, nil)
}

// For some events Claude Code adds hook stdout to the model's context, so
// Run must never write a byte to stdout or stderr.
func TestRunWritesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fd redirection test is unix-only")
	}
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop", "Bogus"} {
		Run(strings.NewReader(payloadJSON(t, map[string]any{"hook_event_name": ev})), time.Now,
			func(wire.Message) error { return errors.New("no daemon") }, nil)
	}
	Run(strings.NewReader("garbage"), time.Now, func(wire.Message) error { return nil }, nil)
	os.Stdout, os.Stderr = origOut, origErr
	_ = outW.Close()
	_ = errW.Close()
	out, _ := io.ReadAll(outR)
	errOut, _ := io.ReadAll(errR)
	if len(out) != 0 || len(errOut) != 0 {
		t.Fatalf("Run wrote stdout=%q stderr=%q", out, errOut)
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// A 10 MiB tool response must not stop us finding fields after it, and the
// whole payload must be consumed so Claude Code's write never fails.
func TestParsePayloadHugeIsDrained(t *testing.T) {
	big := strings.Repeat("x", 10<<20)
	in := `{"tool_response":{"stdout":"` + big + `"},"session_id":"abc-1","hook_event_name":"PostToolUse","tool_name":"Read"}` +
		"\n trailing bytes the hook should still drain"
	cr := &countingReader{r: strings.NewReader(in)}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	p, err := ParsePayload(cr)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if p.Event != "PostToolUse" || p.ToolName != "Read" || p.SessionID != "abc-1" {
		t.Fatalf("got %+v", p)
	}
	if cr.n != len(in) {
		t.Fatalf("drained %d of %d bytes", cr.n, len(in))
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 128<<20 {
		t.Fatalf("allocated %d MiB for a 10 MiB payload", alloc>>20)
	}
}

func TestKindFor(t *testing.T) {
	tests := map[string]model.AgentEventKind{
		"SessionStart":      model.AgentSessionStart,
		"SessionEnd":        model.AgentSessionEnd,
		"UserPromptSubmit":  model.AgentPromptSubmit,
		"PreToolUse":        model.AgentToolStart,
		"PostToolUse":       model.AgentToolEnd,
		"PermissionRequest": model.AgentPermissionAsk,
		"Notification":      model.AgentNotification,
		"Stop":              model.AgentStop,
		"StopFailure":       model.AgentStopFailure,
	}
	for in, want := range tests {
		if got, ok := KindFor(in); !ok || got != want {
			t.Errorf("KindFor(%q) = %q, %v", in, got, ok)
		}
	}
	// SubagentStop is ignored by the tracker, so it isn't mapped or registered.
	for _, unmapped := range []string{"PreCompact", "SubagentStop"} {
		if _, ok := KindFor(unmapped); ok {
			t.Errorf("unexpected kind for unmapped event %q", unmapped)
		}
	}
}
