package classify

import (
	"slices"
	"strings"
	"unicode"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// DomainPrefix marks a domain in exclusions and overrides
// ("domain:github.com"), so app ids and domains share one list.
const DomainPrefix = "domain:"

// NormalizeDomain lower-cases a domain and drops a trailing dot and a
// leading "www.".
func NormalizeDomain(d string) string {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	return strings.TrimPrefix(d, "www.")
}

// IsHostname reports whether s is a dotted host name: two or more labels of
// letters, digits and inner hyphens, ending in a letters-only label.
func IsHostname(s string) bool {
	if len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	labels := strings.Split(s, ".")
	for i, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for j := 0; j < len(l); j++ {
			b := l[j]
			letter := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
			if i == len(labels)-1 && !letter {
				return false
			}
			if !letter && (b < '0' || b > '9') && b != '-' {
				return false
			}
		}
	}
	return len(labels[len(labels)-1]) >= 2
}

// separators split a browser title into the page's parts ("Inbox - Gmail",
// "PR · owner/repo · GitHub").
var separators = []string{" - ", " — ", " – ", " | ", " · ", " • "}

// DomainFromTitle returns the site a browser window's title names, or "".
// Call it on the raw title, in memory: redaction removes the URLs and host
// names it reads. A part naming a known site ("YouTube") wins over a host
// name in the text, and the last such part wins, since sites put their name
// last. Email addresses are skipped: their domain is a person's, not the
// page's.
func DomainFromTitle(title string) string {
	parts := []string{title}
	for _, sep := range separators {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if d, ok := siteNames[strings.ToLower(strings.TrimSpace(parts[i]))]; ok {
			return d
		}
	}
	for _, f := range strings.Fields(title) {
		if strings.Contains(f, "@") {
			continue
		}
		if i := strings.Index(f, "://"); i >= 0 {
			f = f[i+3:]
		}
		if i := strings.IndexAny(f, "/?#:"); i >= 0 {
			f = f[:i]
		}
		f = strings.Trim(f, `()[]{}<>"'.,;!`)
		if IsHostname(f) {
			return NormalizeDomain(f)
		}
	}
	return ""
}

// PrivateTitle reports whether a browser title names a sensitive or
// excluded site anywhere, as whole words in any case: "Accounts Overview |
// Bank of America" or "MyBank - Statements" when mybank.com is excluded.
// Call it on the raw title, in memory.
func (r *Rules) PrivateTitle(title string) bool {
	t := " " + stripBrowserName(words(title)) + " "
	for _, n := range r.privateNames {
		if strings.Contains(t, n) {
			return true
		}
	}
	return false
}

// words lower-cases s and turns every run of other than letters and digits
// into one space.
func words(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// browserNames are the names browsers end their window titles with on
// Windows and Linux ("Inbox - Gmail - Google Chrome"), as words. Longer
// names come first, so "mozilla firefox private browsing" goes whole.
var browserNames = []string{
	"mozilla firefox private browsing", "firefox developer edition", "firefox nightly", "mozilla firefox",
	"google chrome beta", "google chrome dev", "google chrome canary", "google chrome", "chromium",
	"microsoft edge beta", "microsoft edge dev", "microsoft edge canary", "microsoft edge",
	"brave", "vivaldi", "opera",
}

// stripBrowserName drops the browser's name from the end of a title in
// words, so an excluded site's word ("google", "microsoft", "mozilla")
// never matches the browser instead of the page.
func stripBrowserName(t string) string {
	for _, b := range browserNames {
		if t == b {
			return ""
		}
		if s, ok := strings.CutSuffix(t, " "+b); ok {
			return s
		}
	}
	return t
}

// registrable reports whether domain is a whole site (mybank.com,
// example.co.uk) rather than a part of one (mail.google.com). It is a
// guess without the public suffix list: two labels, or three when the
// middle one is generic.
func registrable(domain string) bool {
	labels := strings.Split(domain, ".")
	return len(labels) == 2 || len(labels) == 3 && genericLabels[labels[1]]
}

// genericLabels are second-level labels that name no site ("co" in
// example.co.uk).
var genericLabels = map[string]bool{"co": true, "com": true, "org": true, "net": true, "gov": true, "ac": true, "edu": true}

// siteWord is the word an excluded site's titles are likely to carry: the
// longest label other than the top-level one and generic ones
// ("example" for wiki.example.org, "mybank" for mybank.com).
func siteWord(domain string) string {
	labels := strings.Split(domain, ".")
	best := ""
	for _, l := range labels[:len(labels)-1] {
		if !genericLabels[l] && len(l) > len(best) {
			best = l
		}
	}
	if len(best) < 3 {
		return ""
	}
	return words(best)
}

// Domain returns the category of a site and whether any rule knows it.
// The domain and each parent domain are tried, most specific first:
// exclusions and sensitive sites (private) at any level win, then the
// user's overrides, then the bundled list.
func (r *Rules) Domain(domain string) (model.Category, bool) {
	d := NormalizeDomain(domain)
	if d == "" {
		return "", false
	}
	for _, m := range []map[string]model.Category{r.privateDomains, r.domainOverrides, r.domains} {
		for h := d; ; {
			if c, ok := m[h]; ok {
				return c, true
			}
			i := strings.IndexByte(h, '.')
			if i < 0 {
				break
			}
			h = h[i+1:]
		}
	}
	return "", false
}

// IsBrowser reports whether appID is a bundled browser, whatever the user
// overrode its category to: a browser's titles name sites.
func (r *Rules) IsBrowser(appID string) bool {
	return r.browsers[strings.ToLower(appID)]
}

// SensitiveDomains returns the sites recorded as private by default.
func SensitiveDomains() []string { return slices.Clone(sensitiveDomains) }
