package classify

import (
	"slices"
	"strings"
	"testing"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

func TestDomainFromTitle(t *testing.T) {
	tests := []struct{ title, want string }{
		{"Inbox (3) - jane@chase.com - Gmail", "mail.google.com"}, // the site, not the address
		{"Fix the parser by jane · Pull Request #12 · owner/repo · GitHub", "github.com"},
		{"Lofi beats - YouTube - Google Chrome", "youtube.com"},
		{"Sign in | www.Chase.com.", "chase.com"},
		{"https://bank.example.co.uk/login?next=/ — Mozilla Firefox", "bank.example.co.uk"},
		{"Weekly plan — Notion", "notion.so"},
		{"New Tab", ""},
		{"jane@example.com", ""},
		{"Version 1.2.3", ""},
	}
	for _, tc := range tests {
		if got := DomainFromTitle(tc.title); got != tc.want {
			t.Errorf("DomainFromTitle(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestDomain(t *testing.T) {
	r := New(
		[]string{"domain:Example.org", "com.example.App"},
		map[string]model.Category{"domain:github.com": model.CategoryDocs, "domain:paypal.com": model.CategoryCode, "domain:google.com": model.CategoryOther},
	)
	tests := []struct {
		domain string
		want   model.Category
		known  bool
	}{
		{"mail.google.com", model.CategoryOther, true},  // override on a parent beats bundled on the host
		{"github.com", model.CategoryDocs, true},        // override beats bundled
		{"gist.github.com", model.CategoryDocs, true},   // subdomain
		{"www.paypal.com", model.CategoryPrivate, true}, // sensitive beats override
		{"wiki.example.org", model.CategoryPrivate, true},
		{"youtube.com", model.CategoryMedia, true},
		{"evilyoutube.com", "", false}, // label boundary
		{"unknown.example.com", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, known := r.Domain(tc.domain)
		if got != tc.want || known != tc.known {
			t.Errorf("Domain(%q) = %q, %t; want %q, %t", tc.domain, got, known, tc.want, tc.known)
		}
	}
	if r.App("domain:example.org") == model.CategoryPrivate || r.App("com.example.app") != model.CategoryPrivate {
		t.Error("domain exclusions leaked into app exclusions")
	}
}

func TestIsBrowser(t *testing.T) {
	r := New(nil, map[string]model.Category{"com.google.chrome": model.CategoryCode})
	if !r.IsBrowser("com.google.Chrome") || !r.IsBrowser("FIREFOX.EXE") || r.IsBrowser("com.apple.Terminal") {
		t.Error("IsBrowser wrong")
	}
}

func TestBundledDomainsAreHostnames(t *testing.T) {
	check := func(d string) {
		if !IsHostname(d) || d != NormalizeDomain(d) {
			t.Errorf("%q is not a normalized host name", d)
		}
	}
	for _, ds := range bundledDomains {
		for _, d := range ds {
			check(d)
		}
	}
	for _, d := range sensitiveDomains {
		check(d)
	}
	for name, d := range siteNames {
		check(d)
		if name != strings.ToLower(name) {
			t.Errorf("site name %q is not lower case", name)
		}
	}
}

func TestPrivateTitle(t *testing.T) {
	r := New([]string{"domain:mybank.com", "domain:wiki.example.co.uk", "domain:shop.example.org.uk"}, nil)
	for title, want := range map[string]bool{
		"Team page · Example":                                  false, // wiki.example.co.uk is part of a site
		"Basket · Example":                                     false, // so is shop.example.org.uk
		"Team page | wiki.example.co.uk":                       true,
		"MyBank - Statements - Google Chrome":                  true,
		"Bank of America | Online Banking | Accounts Overview": true,
		"Accounts - Wells Fargo":                               true,
		"CHASE: sign in":                                       true,
		"Inbox (2) - LastPass":                                 true,
		"MyBank - Statements":                                  true, // the excluded site's own word
		"Page on mybank.com":                                   true,
		"A match report - Sport":                               false, // ambiguous words need the domain
		"Chasers of the light":                                 false, // whole words only
		"Pull request · owner/repo · GitHub":                   false,
	} {
		if got := r.PrivateTitle(title); got != want {
			t.Errorf("PrivateTitle(%q) = %t, want %t", title, got, want)
		}
	}
}

// Every sensitive site has a name its titles are matched by, so a page
// that never shows its host name is still private.
func TestSensitiveDomainsHaveNames(t *testing.T) {
	named := map[string]bool{}
	for name, d := range sensitiveNames {
		named[d] = true
		if !slices.Contains(sensitiveDomains, d) {
			t.Errorf("name %q points at %q, which isn't a sensitive domain", name, d)
		}
		if name != words(name) && !strings.Contains(name, ".") {
			t.Errorf("name %q isn't lower-case words", name)
		}
	}
	for _, d := range sensitiveDomains {
		if !named[d] {
			t.Errorf("%s has no name in sensitiveNames", d)
		}
	}
}

// Excluding a site never makes the browser's own name private: Chrome,
// Edge and Firefox end their titles on Windows and Linux with a name
// that carries a site's word ("Google", "Microsoft", "Mozilla").
func TestExclusionsDoNotMatchBrowserNames(t *testing.T) {
	const (
		pr   = "Pull request · owner/repo · GitHub"
		edge = " - Personal - Microsoft\u200b Edge" // Edge puts a zero-width space in its name
	)
	tests := []struct {
		exclude string
		title   string
		want    bool
	}{
		// A subdomain: its parent's word names other sites too.
		{"mail.google.com", pr + " - Google Chrome", false},
		{"mail.google.com", "Design doc - Google Docs - Google Chrome", false},
		{"mail.google.com", "Inbox - mail.google.com - Google Chrome", true},
		{"teams.microsoft.com", pr + edge, false},
		{"teams.microsoft.com", "Chat | teams.microsoft.com" + edge, true},
		{"developer.mozilla.org", pr + " — Mozilla Firefox", false},
		{"developer.mozilla.org", "Array - developer.mozilla.org — Mozilla Firefox Private Browsing", true},
		// A whole site: its word counts, but not in the browser's name.
		{"google.com", pr + " - Google Chrome", false},
		{"google.com", "Google Search - Google Chrome", true},
		{"microsoft.com", pr + edge, false},
		{"microsoft.com", "Microsoft account" + edge, true},
		{"mozilla.org", pr + " — Mozilla Firefox", false},
		{"mozilla.org", "Mozilla Firefox", false},
		{"mozilla.org", "Donate to Mozilla — Mozilla Firefox", true},
		{"mybank.com", "MyBank - Statements - Brave", true},
	}
	for _, tc := range tests {
		r := New([]string{DomainPrefix + tc.exclude}, nil)
		if got := r.PrivateTitle(tc.title); got != tc.want {
			t.Errorf("excluding %s: PrivateTitle(%q) = %t, want %t", tc.exclude, tc.title, got, tc.want)
		}
	}
}
