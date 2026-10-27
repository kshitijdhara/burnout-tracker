package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTitle(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Inbox - jane.doe@example.com - Gmail", "Inbox - [email] - Gmail"},
		{"Pull request #12345 · owner/repo · GitHub", "Pull request #[n] · [path] · GitHub"},
		{"https://bank.example/login?u=jane Sign in", "[url] Sign in"},
		{"www.example.com - Home", "[url] - Home"},
		{"main.go — /Users/jane/src/secret-project — Code", "main.go — [path] — Code"},
		{`report.docx C:\Users\jane\Documents`, "report.docx [path]"},
		{"~/notes/diary.md", "[path]"},
		{"Order 12 of 34, ref 98765", "Order 12 of 34, ref [n]"},
		{"Invoice 2026-10-27", "Invoice [n]-10-27"},
		{"token ghp_aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW", "token [token]"},
		{"commit 3f786850e387550fdab836ed7e6dc881de23001b", "commit [token]"},
		{"key=QUJDREVGR0hJSktMTU5PUFFSU1RVVldY", "[token]"},
		{"Session AbstractSingletonProxyFactoryBean", "Session [token]"},
		{"internationalization and localization", "internationalization and localization"},
		{"Weekly   planning\t— Notion", "Weekly planning — Notion"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := Title(tc.in); got != tc.want {
			t.Errorf("Title(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestTitleBounds(t *testing.T) {
	long := strings.Repeat("word ", 200)
	if n := len(strings.Fields(Title(long))); n != maxFields {
		t.Errorf("%d fields kept, want %d", n, maxFields)
	}
	if got := Title(strings.Repeat("a", 100)); got != Token {
		t.Errorf("a 100-byte field: %q", got)
	}
	if got := Title("caf\xffe"); !utf8.ValidString(got) {
		t.Errorf("invalid UTF-8 kept: %q", got)
	}
}

// FuzzTitle checks the redactor's invariants on any input: the output holds
// nothing a rule removes, is valid UTF-8 and is a fixed point.
func FuzzTitle(f *testing.F) {
	for _, s := range []string{
		"Inbox - jane.doe@example.com - Gmail",
		"https://example.com/a/b?c=123456",
		`C:\Users\jane 12345 ghp_aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW`,
		"१२३४ ١٢٣ 12a345",
		"\xff\xfe a@b.c",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := Title(in)
		if !utf8.ValidString(out) {
			t.Fatalf("invalid UTF-8: %q", out)
		}
		if HasNumber(out) {
			t.Fatalf("a 3+ digit number survived: %q → %q", in, out)
		}
		for _, f := range strings.Fields(out) {
			if IsURL(f) || IsEmail(f) || IsPath(f) || HasToken(f) || len(f) > maxField {
				t.Fatalf("%q survived: %q → %q", f, in, out)
			}
		}
		if again := Title(out); again != out {
			t.Fatalf("not idempotent: %q → %q → %q", in, out, again)
		}
	})
}
