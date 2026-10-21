package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var absTmp, _ = filepath.Abs("tmp-x")

func validShell() Message {
	return Message{V: Version, Kind: KindShellCommand, SentMs: 2, Shell: &ShellCommand{
		Argv0: "git", Subcmd: "commit", Exit: 0, StartMs: 1, EndMs: 2, Cwd: absTmp,
	}}
}

func TestRoundTrip(t *testing.T) {
	b, err := Encode(validShell())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if got.Shell.Argv0 != "git" || got.Shell.Subcmd != "commit" {
		t.Fatalf("round trip lost data: %+v", got.Shell)
	}
}

func TestDecodeRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"too large", `{"v":1,"pad":"` + strings.Repeat("a", MaxMessageBytes) + `"}`, ErrTooLarge},
		{"future version", `{"v":99,"kind":"shell.cmd","shell":{"argv0":"ls","start_ms":1,"end_ms":1}}`, ErrBadVersion},
		{"zero version", `{"kind":"shell.cmd"}`, ErrBadVersion},
		{"unknown kind", `{"v":1,"kind":"nope"}`, ErrMalformed},
		{"missing body", `{"v":1,"kind":"shell.cmd"}`, ErrMalformed},
		{"two bodies", `{"v":1,"kind":"shell.cmd","shell":{"argv0":"ls","start_ms":1,"end_ms":1},"agent":{}}`, ErrMalformed},
		{"trailing data", `{"v":1,"kind":"shell.cmd","shell":{"argv0":"ls","start_ms":1,"end_ms":1}} {}`, ErrMalformed},
		{"not json", `hello`, ErrMalformed},
		{"secret-looking argv0", `{"v":1,"kind":"shell.cmd","shell":{"argv0":"ghp_` + strings.Repeat("x", 70) + `","start_ms":1,"end_ms":1}}`, ErrInvalidData},
		{"argv0 with spaces", `{"v":1,"kind":"shell.cmd","shell":{"argv0":"rm -rf","start_ms":1,"end_ms":1}}`, ErrInvalidData},
		{"end before start", `{"v":1,"kind":"shell.cmd","shell":{"argv0":"ls","start_ms":5,"end_ms":1}}`, ErrInvalidData},
		{"bad agent event", `{"v":1,"kind":"agent.hook","agent":{"agent":"claude","event":"Stop now","at_ms":1}}`, ErrInvalidData},
		{"bad session id", `{"v":1,"kind":"agent.hook","agent":{"agent":"claude","event":"Stop","session_id":"a b","at_ms":1}}`, ErrInvalidData},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tc.in))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUnknownFieldsIgnored(t *testing.T) {
	in := `{"v":1,"kind":"agent.hook","future":true,"agent":{"agent":"claude","event":"Stop","session_id":"abc-123","at_ms":5,"extra":1}}`
	m, err := Decode(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent.Event != "Stop" {
		t.Fatalf("got %+v", m.Agent)
	}
}

func FuzzDecode(f *testing.F) {
	b, _ := Encode(validShell())
	f.Add(b)
	f.Add([]byte(`{"v":1,"kind":"agent.hook","agent":{"agent":"claude","event":"Stop","at_ms":1}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		m, err := Decode(bytes.NewReader(in))
		if err != nil {
			return
		}
		// Anything accepted must re-encode within the size cap.
		if _, err := Encode(m); err != nil && !errors.Is(err, ErrTooLarge) {
			t.Fatalf("accepted message failed to encode: %v", err)
		}
	})
}

func TestPing(t *testing.T) {
	if _, err := Decode(strings.NewReader(`{"v":1,"kind":"ping"}`)); err != nil {
		t.Fatalf("ping rejected: %v", err)
	}
	if _, err := Decode(strings.NewReader(`{"v":1,"kind":"ping","shell":{"argv0":"ls","start_ms":1,"end_ms":1}}`)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("ping with body: %v", err)
	}
}

// TestSchemaMatchesValidation keeps schemas/v1 in step with the Go rules,
// so the published contract never drifts from what the daemon enforces.
func TestSchemaMatchesValidation(t *testing.T) {
	raw, err := os.ReadFile("../../schemas/v1/message.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			MaxLength  int `json:"maxLength"`
			Properties map[string]struct {
				Pattern string `json:"pattern"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]*regexp.Regexp{
		"shell": {"argv0": reCommand, "subcmd": reCommand},
		"agent": {"agent": reAgent, "event": reEvent, "session_id": reSession, "tool_name": reTool},
	}
	for def, fields := range want {
		for field, re := range fields {
			if got := schema.Defs[def].Properties[field].Pattern; got != re.String() {
				t.Errorf("%s.%s: schema pattern %q, code %q", def, field, got, re.String())
			}
		}
	}
	if schema.Defs["cwd"].MaxLength != MaxPathBytes {
		t.Errorf("cwd maxLength %d, code %d", schema.Defs["cwd"].MaxLength, MaxPathBytes)
	}
}

func TestCwdValidation(t *testing.T) {
	abs, _ := filepath.Abs("x")
	for _, tc := range []struct {
		cwd string
		ok  bool
	}{{"", true}, {abs, true}, {"relative/dir", false}, {abs + strings.Repeat("a", MaxPathBytes), false}} {
		m := validShell()
		m.Shell.Cwd = tc.cwd
		b, _ := json.Marshal(m)
		_, err := Decode(bytes.NewReader(b))
		if (err == nil) != tc.ok {
			t.Errorf("cwd %.20q: err %v", tc.cwd, err)
		}
	}
}

func TestIsAbsPathAcceptsPosixFormEverywhere(t *testing.T) {
	for _, p := range []string{"/home/u", "/c/Users/u"} {
		if !IsAbsPath(p) {
			t.Errorf("%s rejected", p)
		}
	}
	if IsAbsPath("rel/dir") {
		t.Error("relative path accepted")
	}
}
