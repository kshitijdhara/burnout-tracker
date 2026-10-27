// Package redact strips window titles of what could identify a person, a
// file or a secret before any classifier sees them (privacy & security
// §4.1). It is pure: the redacted title is still never logged, persisted
// or hashed into storage.
package redact

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Placeholders stand in for what was removed, so a classifier still sees
// that something was there and two numbers never merge into one.
const (
	URL    = "[url]"
	Email  = "[email]"
	Path   = "[path]"
	Token  = "[token]"
	Number = "[n]"
)

// Limits: a title is a few words. Longer input is cut at a field boundary,
// and a field longer than maxField is no word anyone reads.
const (
	maxFields = 64
	maxField  = 64
	minDigits = 3
	minToken  = 20
)

// Title returns t with every email address, URL, file path, number of 3 or
// more digits and identifier- or secret-like token of 20 or more
// characters replaced by a placeholder. Whitespace is collapsed to single
// spaces. Title(Title(t)) == Title(t).
func Title(t string) string {
	fields := strings.Fields(strings.ToValidUTF8(t, string(utf8.RuneError)))
	if len(fields) > maxFields {
		fields = fields[:maxFields]
	}
	for i, f := range fields {
		fields[i] = field(f)
	}
	return strings.Join(fields, " ")
}

func field(f string) string {
	switch {
	case IsURL(f):
		return URL
	case IsEmail(f):
		return Email
	case IsPath(f):
		return Path
	case len(f) > maxField || HasToken(f):
		return Token
	}
	return numbers(f)
}

// IsURL reports whether a whitespace-free field is or contains a URL.
func IsURL(f string) bool {
	l := strings.ToLower(f)
	return strings.Contains(l, "://") || strings.HasPrefix(l, "www.") || strings.HasPrefix(l, "mailto:")
}

// IsEmail reports whether a whitespace-free field contains an address:
// something, an @, then a dotted part.
func IsEmail(f string) bool {
	i := strings.IndexByte(f, '@')
	return i > 0 && strings.Contains(f[i+1:], ".")
}

// IsPath reports whether a whitespace-free field looks like a file path:
// it has a path separator or starts at the home folder.
func IsPath(f string) bool {
	return strings.ContainsAny(f, `/\`) || strings.HasPrefix(f, "~")
}

// HasToken reports whether f holds a run of 20 or more id-like characters
// that looks like an identifier or a secret: hex with a digit, two or more
// character classes (camelCase, snake_case, base64), or near-random
// letters. Long ordinary words ("internationalization") are kept.
func HasToken(f string) bool {
	start := -1
	for i := 0; i <= len(f); i++ {
		if i < len(f) && isTokenByte(f[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start >= minToken && secretLike(f[start:i]) {
			return true
		}
		start = -1
	}
	return false
}

func isTokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' ||
		b == '_' || b == '-' || b == '+' || b == '='
}

func secretLike(run string) bool {
	var lower, upper, digit, symbol, hex bool
	hex = true
	for i := 0; i < len(run); i++ {
		switch b := run[i]; {
		case b >= 'a' && b <= 'z':
			lower = true
			hex = hex && b <= 'f'
		case b >= 'A' && b <= 'Z':
			upper = true
			hex = hex && b <= 'F'
		case b >= '0' && b <= '9':
			digit = true
		case b == '-':
			hex = false // hyphens join words; they are no character class
		default:
			symbol = true
			hex = false
		}
	}
	classes := 0
	for _, has := range []bool{lower, upper, digit, symbol} {
		if has {
			classes++
		}
	}
	return hex && digit || classes >= 2 || entropy(run) >= 3.9
}

// entropy is the Shannon entropy of s in bits per byte.
func entropy(s string) float64 {
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	var h float64
	n := float64(len(s))
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / n
			h -= p * math.Log2(p)
		}
	}
	return h
}

// numbers replaces each run of 3 or more digits with Number.
func numbers(f string) string {
	var b strings.Builder
	run := 0
	flush := func(digits string) {
		if run >= minDigits {
			b.WriteString(Number)
		} else {
			b.WriteString(digits)
		}
		run = 0
	}
	start := 0
	for i, r := range f {
		if unicode.IsDigit(r) {
			if run == 0 {
				start = i
			}
			run++
			continue
		}
		if run > 0 {
			flush(f[start:i])
		}
		b.WriteRune(r)
	}
	if run > 0 {
		flush(f[start:])
	}
	return b.String()
}

// HasNumber reports whether s has a run of 3 or more digits.
func HasNumber(s string) bool {
	run := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			if run++; run >= minDigits {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}
