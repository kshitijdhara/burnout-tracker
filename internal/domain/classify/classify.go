// Package classify maps apps and commands to categories using bundled
// rules (system design §4.6, cascade steps 1–3). The optional Laya step
// lives in an adapter; this package is pure.
package classify

import (
	"maps"
	"slices"
	"strings"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// Rules classifies app identifiers. The zero value is not usable; call New.
type Rules struct {
	exact     map[string]model.Category // lower-cased app id or exe name
	prefixes  []prefixRule
	private   map[string]bool // sensitive denylist + user exclusions, lower-cased
	overrides map[string]model.Category
	browsers  map[string]bool // bundled browsers, whatever their override

	// Sites, for browser titles (domains.go).
	domains         map[string]model.Category
	domainOverrides map[string]model.Category
	privateDomains  map[string]model.Category // all CategoryPrivate
	privateNames    []string                  // padded words (" bank of america "), see PrivateTitle
}

type prefixRule struct {
	prefix string
	cat    model.Category
}

// New returns the bundled rules plus the user's exclusions (recorded as
// private) and overrides (which win over bundled rules but not over
// exclusions). Entries starting with DomainPrefix name sites rather than
// apps.
func New(exclusions []string, overrides map[string]model.Category) *Rules {
	r := &Rules{
		exact:           make(map[string]model.Category, len(bundledApps)),
		private:         make(map[string]bool, len(sensitiveApps)+len(exclusions)),
		overrides:       make(map[string]model.Category, len(overrides)),
		browsers:        make(map[string]bool, len(bundledApps[model.CategoryBrowser])),
		domains:         make(map[string]model.Category),
		domainOverrides: make(map[string]model.Category),
		privateDomains:  make(map[string]model.Category, len(sensitiveDomains)),
	}
	for cat, ids := range bundledApps {
		for _, id := range ids {
			r.exact[strings.ToLower(id)] = cat
			if cat == model.CategoryBrowser {
				r.browsers[strings.ToLower(id)] = true
			}
		}
	}
	for cat, ds := range bundledDomains {
		for _, d := range ds {
			r.domains[d] = cat
		}
	}
	for _, d := range sensitiveDomains {
		r.privateDomains[d] = model.CategoryPrivate
	}
	for name := range sensitiveNames {
		r.privateNames = append(r.privateNames, " "+words(name)+" ")
	}
	for _, p := range bundledPrefixes {
		r.prefixes = append(r.prefixes, prefixRule{strings.ToLower(p.prefix), p.cat})
	}
	for _, id := range sensitiveApps {
		r.private[strings.ToLower(id)] = true
	}
	for _, id := range exclusions {
		if d, ok := cutDomain(id); ok {
			r.privateDomains[d] = model.CategoryPrivate
			r.privateNames = append(r.privateNames, " "+words(d)+" ")
			if n := siteWord(d); n != "" {
				r.privateNames = append(r.privateNames, " "+n+" ")
			}
			continue
		}
		r.private[strings.ToLower(id)] = true
	}
	for id, cat := range overrides {
		if d, ok := cutDomain(id); ok {
			r.domainOverrides[d] = cat
			continue
		}
		r.overrides[strings.ToLower(id)] = cat
	}
	return r
}

// cutDomain returns the normalized domain of a "domain:" entry.
func cutDomain(entry string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(entry)), DomainPrefix)
	return NormalizeDomain(rest), ok
}

// App returns the category for an app id (bundle id or executable name).
// Private apps return CategoryPrivate; callers must then drop the app id.
func (r *Rules) App(appID string) model.Category {
	id := strings.ToLower(appID)
	if r.private[id] {
		return model.CategoryPrivate
	}
	if c, ok := r.overrides[id]; ok {
		return c
	}
	if c, ok := r.exact[id]; ok {
		return c
	}
	for _, p := range r.prefixes {
		if strings.HasPrefix(id, p.prefix) {
			return p.cat
		}
	}
	return model.CategoryUncategorized
}

// CommandCategory groups shell commands.
type CommandCategory string

// Command categories.
const (
	CmdVCS    CommandCategory = "vcs"
	CmdBuild  CommandCategory = "build"
	CmdInfra  CommandCategory = "infra"
	CmdRemote CommandCategory = "remote"
	CmdEditor CommandCategory = "editor"
	CmdAgent  CommandCategory = "agent"
	CmdOther  CommandCategory = "other"
)

// Command returns the category of a shell command name.
func Command(argv0 string) CommandCategory {
	if c, ok := commandCategories[argv0]; ok {
		return c
	}
	return CmdOther
}

// Subcommand returns sub if it is a known subcommand of argv0, else "".
// The shell hook sends a candidate word for allowlisted tools; only known
// subcommands are kept, so a stray argument (a path, a token) is dropped.
func Subcommand(argv0, sub string) string {
	if subcommands[argv0][sub] {
		return sub
	}
	return ""
}

// Subcommands returns a copy of the subcommand allowlist, tool → sorted
// subcommands. The shell hook renders it into the hook script, so words not
// listed never leave the shell; the daemon checks them again on receipt.
func Subcommands() map[string][]string {
	out := make(map[string][]string, len(subcommands))
	for tool, subs := range subcommands {
		out[tool] = slices.Sorted(maps.Keys(subs))
	}
	return out
}

// SensitiveApps returns the apps recorded as private by default.
func SensitiveApps() []string { return slices.Clone(sensitiveApps) }
