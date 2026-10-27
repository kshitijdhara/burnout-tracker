package classify

import (
	"strings"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
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
