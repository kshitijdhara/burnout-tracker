package app

import (
	"github.com/kshitijdhara/burntout/internal/domain/classify"
	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// titleRules classifies browser windows by title (privacy & security §4.1,
// system design §4.6). The raw title and its domain live only in memory,
// for one call; only the category leaves.
//
// There is no cache yet: every step is a map lookup or a scan of the
// title, so a cache would cost a redaction and an HMAC per window per tick
// to save nothing. It comes with the optional Laya step (ADR-0006), keyed
// by HMAC of the browser, the site and the redacted title; the Hasher is
// taken now for that.
type titleRules struct {
	rules *classify.Rules
}

func newTitleRules(rules *classify.Rules, _ Hasher) *titleRules {
	return &titleRules{rules: rules}
}

// category returns w's category. Without a title, or for an app other than
// a browser, it is the app's category. A browser title goes through the
// cascade: the app's exclusion, then a sensitive or excluded site named
// anywhere in the title, then the title's site (exclusions and sensitive
// sites are private, whatever the user's overrides say), the user's site
// overrides and the bundled sites, then the app's own category.
func (t *titleRules) category(w model.Window) model.Category {
	cat := t.rules.App(w.AppID)
	title := w.Title.Reveal()
	if cat == model.CategoryPrivate || title == "" || !t.rules.IsBrowser(w.AppID) {
		return cat
	}
	if t.rules.PrivateTitle(title) {
		return model.CategoryPrivate
	}
	if c, ok := t.rules.Domain(classify.DomainFromTitle(title)); ok {
		return c
	}
	// The optional Laya step (ADR-0006) slots in here. Until then the
	// app's category stands.
	return cat
}
