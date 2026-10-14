// Package redact removes credentials from remote URLs and git messages
// before anything is stored, logged or printed
// (docs/spec/git-invocation.md#url-scrubbing).
//
// It is part of the functional core: pure functions over strings, with no
// I/O. Clones that keep a token in their remote URL are a common way tokens
// leak, and analysis would otherwise copy that token into every warning,
// report and log line, so every remote string passes through URL and every
// git message through Message.
package redact

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Unparsed replaces a remote URL that cannot be parsed. The raw string is
// never written anywhere, because it may hold a credential.
const Unparsed = "<unparsed-remote>"

// maxMessage is the length, in runes, of a stored git error message.
const maxMessage = 180

var (
	// schemeURL matches the start of a URL with a scheme, such as https://,
	// ssh:// or git+ssh:// (RFC 3986 scheme syntax).
	schemeURL = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

	// urlToken matches a URL with a scheme inside free text. Quotes, angle
	// brackets and backticks end the token, because git quotes URLs in its
	// messages: unable to access 'https://...'.
	urlToken = regexp.MustCompile("[A-Za-z][A-Za-z0-9+.-]*://[^\\s'\"<>`]+")

	// userinfo matches the scheme and userinfo of a URL inside free text:
	// everything between :// and the last @ before the authority ends. It
	// is the belt to urlToken's braces: it also catches a password that
	// contains a quote, which ends a urlToken early.
	userinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/?#\s@]*@`)

	// scpPassword matches a "user:password@" prefix on a remote without a
	// scheme. git would read that as an scp-like host, so it never
	// authenticates anything, but the text may still be a credential.
	scpPassword = regexp.MustCompile(`^[^@/:\s]+:[^@/\s]*@`)
)

// URL returns raw with any userinfo removed:
// "https://user:TOKEN@github.com/o/r.git" becomes
// "https://github.com/o/r.git", and ssh://, git+ssh:// and every other scheme
// are treated the same way. The scp form "git@host:path" carries no password
// and is kept as git printed it, as is a local path. A URL with a scheme that
// does not parse, or any input with a control character, becomes Unparsed.
// The empty string, meaning no remote, stays empty.
func URL(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 || !utf8.ValidString(raw) {
		return Unparsed
	}
	if !schemeURL.MatchString(raw) {
		return scpPassword.ReplaceAllString(raw, "")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Unparsed
	}
	u.User = nil
	return u.String()
}

// Message prepares free text, typically git's stderr, for storage: URL is
// applied to every URL in it, any remaining "scheme://user:pass@" is
// stripped, control characters and runs of whitespace collapse to single
// spaces, and the result is truncated to 180 runes, ending in "…" when
// something was cut. Scrubbing happens before truncation, so a credential is
// never cut into a fragment the scrubber no longer recognises.
func Message(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = userinfo.ReplaceAllString(s, "$1")
	s = urlToken.ReplaceAllStringFunc(s, URL)
	s = strings.Join(strings.Fields(strings.Map(controlToSpace, s)), " ")
	if utf8.RuneCountInString(s) <= maxMessage {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxMessage-1]) + "…"
}

func controlToSpace(r rune) rune {
	if unicode.IsControl(r) {
		return ' '
	}
	return r
}
