package claude

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"
)

const payloadCanary = "CANARYpayload7"

func TestParsePayloadSkipsEverythingElse(t *testing.T) {
	in := `{
	  "session_id": "abc-123",
	  "prompt": "tell me \"` + payloadCanary + `\" \u0041\\ and more",
	  "tool_input": {"command": "echo ` + payloadCanary + `", "nested": [1, -2.5e3, true, null, {"k": ["` + payloadCanary + `"]}], "q": "\\\"}"},
	  "tool_response": ["` + payloadCanary + `", {"x": "]}\""}],
	  "hook_event_name": "PreToolUse",
	  "tool_name": "Bash",
	  "cwd": "/home/u/proj",
	  "transcript_path": "/tmp/` + payloadCanary + `.jsonl"
	}`
	p, err := ParsePayload(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "abc-123" || p.Event != "PreToolUse" || p.ToolName != "Bash" || p.Cwd.Reveal() != "/home/u/proj" {
		t.Fatalf("got %+v cwd=%q", p, p.Cwd.Reveal())
	}
}

func TestParsePayloadUnescapesKeptValues(t *testing.T) {
	p, err := ParsePayload(strings.NewReader(`{"cwd":"C:\\Users\\caf\u00e9","hook_event_name":"Stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Cwd.Reveal(); got != `C:\Users\café` {
		t.Fatalf("cwd = %q", got)
	}
}

func TestParsePayloadIgnoresWrongTypesAndOversizedValues(t *testing.T) {
	in := `{"session_id": 42, "tool_name": {"a": 1}, "cwd": "` + strings.Repeat("x", maxValueBytes+10) + `", "hook_event_name": "Stop"}`
	p, err := ParsePayload(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if p.SessionID != "" || p.ToolName != "" || p.Cwd.Reveal() != "" || p.Event != "Stop" {
		t.Fatalf("got %+v", p)
	}
}

func TestParsePayloadRejectsMalformed(t *testing.T) {
	for _, in := range []string{
		``, `[]`, `{`, `{"a"}`, `{"a":}`, `{"a":1,}`, `{"a":"x`, `{"a":{"b":[1,2}`, `{"a":@}`,
		"{\"a\":\"line\nbreak\"}", `{"hook_event_name":"Stop" "x":1}`, `{1:2}`,
	} {
		if _, err := ParsePayload(strings.NewReader(in)); !errors.Is(err, errPayload) {
			t.Errorf("ParsePayload(%q) err = %v, want malformed", in, err)
		}
	}
	if p, err := ParsePayload(strings.NewReader(`  {}  trailing`)); err != nil || p != (Payload{}) {
		t.Fatalf("empty object: %+v %v", p, err)
	}
}

// A 10 MiB prompt must stream past without being held in memory.
func TestParsePayloadDoesNotBufferSkippedValues(t *testing.T) {
	huge := strings.Repeat("a", 10<<20)
	in := []byte(`{"session_id":"s","prompt":"` + huge + `","tool_response":{"x":["` + huge + `"]},"hook_event_name":"Stop"}`)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p, err := ParsePayload(bytes.NewReader(in))
	runtime.ReadMemStats(&after)
	if err != nil || p.Event != "Stop" {
		t.Fatalf("%+v %v", p, err)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
		t.Fatalf("parsing a 20 MiB payload allocated %d bytes; skipped values are being buffered", alloc)
	}
}

func FuzzParsePayload(f *testing.F) {
	f.Add([]byte(`{"session_id":"s","prompt":"` + payloadCanary + `","hook_event_name":"Stop","cwd":"/x"}`))
	f.Add([]byte(`{"tool_input":{"a":["` + payloadCanary + `",{"b":"\"` + payloadCanary + `"}]},"tool_name":"Bash"}`))
	f.Add([]byte(`{"x":"\u0041` + payloadCanary + `\\"}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		p, err := ParsePayload(bytes.NewReader(in))
		if err != nil {
			return
		}
		// Only allowlisted keys can contribute. Strip the allowlisted
		// fields from the input; whatever the payload holds must appear in
		// one of them, so a payloadCanary only in other fields can never leak.
		for _, v := range []string{p.SessionID, p.Event, p.ToolName, p.Cwd.Reveal()} {
			if strings.Contains(v, payloadCanary) && !allowlistedHolds(in, payloadCanary) {
				t.Fatalf("payloadCanary from a non-allowlisted field leaked: %+v", p)
			}
		}
	})
}

// allowlistedHolds reports whether payloadCanary appears right after an
// allowlisted key in the input (the only way it may legitimately be kept).
func allowlistedHolds(in []byte, payloadCanary string) bool {
	s := string(in)
	for _, k := range []string{`"session_id"`, `"hook_event_name"`, `"tool_name"`, `"cwd"`} {
		for i := strings.Index(s, k); i >= 0; {
			rest := s[i+len(k):]
			if j := strings.Index(rest, payloadCanary); j >= 0 && !strings.ContainsAny(rest[:j], ",{}[]") {
				return true
			}
			n := strings.Index(rest, k)
			if n < 0 {
				break
			}
			i += len(k) + n
		}
	}
	return false
}
