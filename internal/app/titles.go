package app

import (
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/redact"
)

// maxTitleCache bounds the title cache. It is cleared, not evicted, when
// full: entries are cheap to recompute.
const maxTitleCache = 4096

// titleRules classifies browser windows by title (privacy & security §4.1,
// system design §4.6). The raw title, its domain and the redacted title
// live only in memory, for one call; only the category leaves. The cache
// is keyed by HMAC and is never written anywhere.
type titleRules struct {
	rules  *classify.Rules
	hasher Hasher
	cache  map[string]model.Category
}

func newTitleRules(rules *classify.Rules, h Hasher) *titleRules {
	return &titleRules{rules: rules, hasher: h, cache: make(map[string]model.Category)}
}

// category returns w's category. Without a title, or for an app other than
// a browser, it is the app's category. A browser title goes through the
// cascade: the app's exclusion, then the site's (exclusions and sensitive
// sites are private, whatever the user's overrides say), the user's site
// overrides and the bundled sites, then the cache of earlier results, then
// the app's own category.
func (t *titleRules) category(w model.Window) model.Category {
	cat := t.rules.App(w.AppID)
	title := w.Title.Reveal()
	if cat == model.CategoryPrivate || title == "" || !t.rules.IsBrowser(w.AppID) {
		return cat
	}
	domain := classify.DomainFromTitle(title)
	if c, ok := t.rules.Domain(domain); ok {
		return c
	}
	// Two pages with the same redacted title on different sites
	// ("Sign in") must not share an entry.
	key := t.hasher.Key("title", w.AppID, domain, redact.Title(title))
	if c, ok := t.cache[key]; ok {
		return c
	}
	// The optional Laya step (ADR-0006) slots in here. Until then the
	// app's category stands.
	if len(t.cache) >= maxTitleCache {
		clear(t.cache)
	}
	t.cache[key] = cat
	return cat
}
