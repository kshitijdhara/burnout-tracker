package classify

import "github.com/kshitijdhara/burnout-tracker/internal/domain/model"

// bundledDomains maps sites to categories, for browser windows when title
// classification is on. A domain also covers its subdomains; the most
// specific match wins (mail.google.com is comms, docs.google.com docs).
var bundledDomains = map[model.Category][]string{
	model.CategoryCode: {
		"github.com", "gitlab.com", "bitbucket.org", "codeberg.org", "stackoverflow.com", "stackexchange.com",
		"go.dev", "pkg.go.dev", "docs.python.org", "pypi.org", "crates.io", "docs.rs", "npmjs.com",
		"developer.mozilla.org", "developer.apple.com", "learn.microsoft.com", "console.cloud.google.com",
		"console.aws.amazon.com", "portal.azure.com", "vercel.com", "netlify.com", "kaggle.com", "colab.research.google.com",
	},
	model.CategoryComms: {
		"mail.google.com", "outlook.live.com", "outlook.office.com", "outlook.office365.com", "app.slack.com",
		"discord.com", "web.whatsapp.com", "web.telegram.org", "teams.microsoft.com", "teams.live.com",
		"mail.proton.me", "fastmail.com", "messages.google.com", "chat.google.com",
	},
	model.CategoryMeeting: {
		"meet.google.com", "zoom.us", "whereby.com", "webex.com", "meet.jit.si",
	},
	model.CategoryDocs: {
		"docs.google.com", "drive.google.com", "calendar.google.com", "notion.so", "notion.site", "figma.com",
		"atlassian.net", "linear.app", "miro.com", "coda.io", "airtable.com", "trello.com", "asana.com",
		"overleaf.com", "scholar.google.com", "arxiv.org", "wikipedia.org", "zotero.org", "office.com",
		"sharepoint.com", "onedrive.live.com", "quip.com", "dropbox.com",
	},
	model.CategoryMedia: {
		"youtube.com", "netflix.com", "twitch.tv", "open.spotify.com", "music.apple.com", "tv.apple.com",
		"primevideo.com", "disneyplus.com", "hulu.com", "max.com", "vimeo.com", "soundcloud.com",
		"reddit.com", "x.com", "twitter.com", "instagram.com", "facebook.com", "tiktok.com",
	},
}

// sensitiveDomains are sites recorded as private (privacy & security
// §5.1): banking, payments, investing and crypto, health and therapy,
// dating, and password-manager vaults. Their subdomains are covered too.
// Users add their own with privacy.exclude_domains.
var sensitiveDomains = []string{
	// Banking, payments and tax.
	"paypal.com", "venmo.com", "wise.com", "revolut.com", "monzo.com", "n26.com", "starlingbank.com",
	"chase.com", "bankofamerica.com", "wellsfargo.com", "citi.com", "capitalone.com", "usbank.com",
	"americanexpress.com", "discover.com", "hsbc.com", "barclays.co.uk", "lloydsbank.com", "natwest.com",
	"santander.co.uk", "ing.com", "intuit.com", "irs.gov", "hmrc.gov.uk",
	// Investing and crypto.
	"schwab.com", "fidelity.com", "vanguard.com", "robinhood.com", "etrade.com", "coinbase.com",
	"binance.com", "kraken.com",
	// Health and therapy.
	"mychart.com", "mychart.org", "zocdoc.com", "webmd.com", "betterhelp.com", "talkspace.com",
	"headspace.com", "calm.com", "teladoc.com", "patient.info",
	// Dating.
	"tinder.com", "bumble.com", "hinge.co", "okcupid.com", "match.com", "grindr.com", "feeld.co",
	// Password-manager vaults.
	"1password.com", "bitwarden.com", "lastpass.com", "dashlane.com", "keepersecurity.com",
}

// siteNames maps the name a site puts in its page titles ("Inbox - Gmail")
// to its domain. Keys are lower case.
var siteNames = map[string]string{
	"github": "github.com", "gitlab": "gitlab.com", "stack overflow": "stackoverflow.com",
	"gmail": "mail.google.com", "outlook": "outlook.office.com", "slack": "app.slack.com", "discord": "discord.com",
	"whatsapp": "web.whatsapp.com", "microsoft teams": "teams.microsoft.com",
	"meet": "meet.google.com", "google meet": "meet.google.com", "zoom": "zoom.us",
	"google docs": "docs.google.com", "google sheets": "docs.google.com", "google slides": "docs.google.com",
	"google drive": "drive.google.com", "google calendar": "calendar.google.com", "notion": "notion.so",
	"figma": "figma.com", "jira": "atlassian.net", "confluence": "atlassian.net", "linear": "linear.app",
	"overleaf": "overleaf.com", "google scholar": "scholar.google.com", "wikipedia": "wikipedia.org",
	"youtube": "youtube.com", "netflix": "netflix.com", "twitch": "twitch.tv", "spotify": "open.spotify.com",
	"reddit": "reddit.com", "x": "x.com", "instagram": "instagram.com", "facebook": "facebook.com",
}

// sensitiveNames are the words a sensitive site's titles carry. A title
// with one of them anywhere, as whole words and in any case, is private:
// matching too much loses detail, never privacy. Every sensitive domain
// has at least one; ambiguous words are given as the domain ("wise.com").
var sensitiveNames = map[string]string{
	"paypal": "paypal.com", "venmo": "venmo.com", "wise.com": "wise.com", "revolut": "revolut.com",
	"monzo": "monzo.com", "n26": "n26.com", "starling bank": "starlingbank.com", "chase": "chase.com",
	"bank of america": "bankofamerica.com", "wells fargo": "wellsfargo.com", "citi": "citi.com",
	"citibank": "citi.com", "capital one": "capitalone.com", "us bank": "usbank.com",
	"american express": "americanexpress.com", "amex": "americanexpress.com", "discover card": "discover.com",
	"hsbc": "hsbc.com", "barclays": "barclays.co.uk", "lloyds bank": "lloydsbank.com", "natwest": "natwest.com",
	"santander": "santander.co.uk", "ing.com": "ing.com", "intuit": "intuit.com", "turbotax": "intuit.com",
	"quickbooks": "intuit.com", "irs": "irs.gov", "hmrc": "hmrc.gov.uk",
	"schwab": "schwab.com", "fidelity": "fidelity.com", "vanguard": "vanguard.com", "robinhood": "robinhood.com",
	"etrade": "etrade.com", "e trade": "etrade.com", "coinbase": "coinbase.com", "binance": "binance.com",
	"kraken":  "kraken.com",
	"mychart": "mychart.com", "mychart.org": "mychart.org", "zocdoc": "zocdoc.com", "webmd": "webmd.com",
	"betterhelp": "betterhelp.com", "talkspace": "talkspace.com", "headspace": "headspace.com",
	"calm.com": "calm.com", "teladoc": "teladoc.com", "patient.info": "patient.info",
	"tinder": "tinder.com", "bumble": "bumble.com", "hinge": "hinge.co", "okcupid": "okcupid.com",
	"match.com": "match.com", "grindr": "grindr.com", "feeld": "feeld.co",
	"1password": "1password.com", "bitwarden": "bitwarden.com", "lastpass": "lastpass.com",
	"dashlane": "dashlane.com", "keeper security": "keepersecurity.com",
}
