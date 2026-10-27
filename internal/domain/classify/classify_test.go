package classify

import (
	"strings"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

func TestApp(t *testing.T) {
	r := New([]string{"com.example.Secret"}, map[string]model.Category{"notion.id": model.CategoryComms, "com.example.secret": model.CategoryCode})
	tests := []struct {
		app  string
		want model.Category
	}{
		{"com.googlecode.iterm2", model.CategoryTerminal},
		{"WINDOWSTERMINAL.EXE", model.CategoryTerminal}, // case-insensitive
		{"org.gnome.Ptyxis", model.CategoryTerminal},    // GNOME Wayland app ids
		{"org.gnome.Console", model.CategoryTerminal},
		{"org.gnome.Terminal", model.CategoryTerminal},
		{"com.jetbrains.goland", model.CategoryCode}, // prefix rule
		{"com.1password.1password", model.CategoryPrivate},
		{"com.example.Secret", model.CategoryPrivate}, // exclusion beats override
		{"notion.id", model.CategoryComms},            // override beats bundled
		{"com.unknown.app", model.CategoryUncategorized},
	}
	for _, tc := range tests {
		if got := r.App(tc.app); got != tc.want {
			t.Errorf("App(%q) = %q, want %q", tc.app, got, tc.want)
		}
	}
}

func TestCommandAndSubcommand(t *testing.T) {
	if Command("kubectl") != CmdInfra || Command("ls") != CmdOther {
		t.Fatal("command categories wrong")
	}
	tests := []struct{ argv0, sub, want string }{
		{"git", "commit", "commit"},
		{"git", "ghp_notasubcommand", ""}, // stray token dropped
		{"git", "/home/me/repo", ""},
		{"make", "deploy", ""}, // user-defined targets are never kept
		{"ls", "anything", ""},
	}
	for _, tc := range tests {
		if got := Subcommand(tc.argv0, tc.sub); got != tc.want {
			t.Errorf("Subcommand(%q, %q) = %q, want %q", tc.argv0, tc.sub, got, tc.want)
		}
	}
}

func TestSensitiveAppsArePrivate(t *testing.T) {
	r := New([]string{"com.Spotify.Client"}, map[string]model.Category{"com.spotify.client": model.CategoryComms})
	for _, id := range append(SensitiveApps(), "COM.LEDGER.LIVE", "com.spotify.client") {
		if got := r.App(id); got != model.CategoryPrivate {
			t.Errorf("%s: %s, want private", id, got)
		}
	}
	seen := map[string]bool{}
	for _, id := range SensitiveApps() {
		if seen[strings.ToLower(id)] {
			t.Errorf("%s listed twice", id)
		}
		seen[strings.ToLower(id)] = true
	}
}
