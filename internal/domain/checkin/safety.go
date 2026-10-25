package checkin

import "strings"

// Resource is a place to get support.
type Resource struct {
	Name, Contact string
}

// SupportMessage is shown when a check-in lands in the high band (system
// design §8.4). It is supportive and never alarming. A mental-health
// professional must review this copy before the alpha.
const SupportMessage = `Your answers suggest you've been carrying a lot lately. That's worth
taking seriously, and you don't have to sort it out alone.

  • Talk to someone you trust about how you've been feeling.
  • A doctor or counsellor can help, and many employers offer a free,
    confidential assistance programme.
  • If you ever feel unsafe or think about harming yourself, contact
    emergency services or one of these lines now:`

// resources are free support lines by region (ISO 3166 code). Only lines
// that are free, national and long-running are listed.
var resources = map[string][]Resource{
	"US": {{"988 Suicide & Crisis Lifeline", "call or text 988"}},
	"CA": {{"9-8-8 Suicide Crisis Helpline", "call or text 988"}},
	"GB": {{"Samaritans", "call 116 123 (free, 24/7)"}},
	"IE": {{"Samaritans", "call 116 123 (free, 24/7)"}},
	"AU": {{"Lifeline", "call 13 11 14"}},
	"NZ": {{"Need to talk?", "call or text 1737"}},
	"IN": {{"Tele-MANAS", "call 14416 (free, 24/7)"}},
	"DE": {{"TelefonSeelsorge", "call 0800 111 0 111 or 0800 111 0 222 (free, 24/7)"}},
}

// Directory covers every other country.
var Directory = Resource{"Find a Helpline (free lines worldwide)", "https://findahelpline.com"}

// Resources returns the support lines for a region, followed by the
// worldwide directory. An unknown region gets the directory only.
func Resources(region string) []Resource {
	return append(append([]Resource(nil), resources[strings.ToUpper(region)]...), Directory)
}

// RegionFromLocale extracts the territory from a POSIX locale such as
// "en_IN.UTF-8" or "de_DE@euro"; "" when there is none.
func RegionFromLocale(locale string) string {
	if i := strings.IndexAny(locale, ".@"); i >= 0 {
		locale = locale[:i]
	}
	_, territory, ok := strings.Cut(locale, "_")
	if !ok || len(territory) != 2 {
		return ""
	}
	return strings.ToUpper(territory)
}
