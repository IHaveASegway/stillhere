// Package redact keeps out of an output what must not reach it: it removes
// credentials from remote URLs and git messages before anything is stored,
// logged or printed (docs/spec/git-invocation.md#url-scrubbing), and finds
// the email addresses that a page or an answer withholds (address.go).
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
	// everything between :// and the last @ before the authority ends at /,
	// ?, # or whitespace. It is the belt to urlToken's braces: it also
	// removes a password that contains a quote, which ends a urlToken early,
	// or a raw @.
	userinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/?#\s]*@`)

	// scpPassword matches a "user:password@" prefix on a remote without a
	// scheme. git would read that as an scp-like host, so it never
	// authenticates anything, but the text may still be a credential.
	scpPassword = regexp.MustCompile(`^[^@/:\s]+:[^@/\s]*@`)

	// scpRemote matches a clean scp-like remote, "user@host:path": the only
	// @ ends a user name that has no colon, so it carries no password.
	scpRemote = regexp.MustCompile(`^[^@/:\s]+@[^@/:\s]+:[^@\s]*$`)

	// collapsedUserinfo matches the scheme and userinfo of a URL whose "//"
	// a path cleaner collapsed to one separator. That is what a URL given
	// where a path is expected becomes: filepath.Abs turns
	// "https://user:TOKEN@host/r.git" into "/cwd/https:/user:TOKEN@host/r.git"
	// ("\" on Windows), which neither urlToken nor userinfo can see. Group 1
	// is the scheme and its separator.
	collapsedUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*:[/\\])[^/\\?#\s]*@`)

	// collapsedQuery matches the query and fragment of a URL whose "//" a
	// path cleaner collapsed, "/cwd/https:/host/r.git?access_token=TOKEN",
	// which URL removes from the URL as typed: either can carry a token.
	// Group 1 is the URL before them. Group 2 ends the match and is kept:
	// whitespace, a quote, or the end of the text, after the ":" that joins
	// a path to its message ("<path>: does not exist") when there is one.
	// The scheme has two characters or more, because a one-letter one is a
	// Windows drive, as git itself reads it (has_dos_drive_prefix):
	// "C:\src\C#\app" is a path, and keeps its "#". The pattern also
	// matches a URL whose "//" is intact, where it removes what URL would.
	collapsedQuery = regexp.MustCompile("([A-Za-z][A-Za-z0-9+.-]+:[/\\\\][^\\s'\"<>`?#]*)[?#][^\\s'\"<>`]*?(:?(?:[\\s'\"<>`]|$))")

	// textPassword matches a "user:password@" prefix on a token without a
	// scheme inside free text, such as an scp-like remote git quotes in
	// "fatal: 'user:pw@host:o/r.git' does not appear to be a git
	// repository", or one that starts a segment of a path, as an scp-like
	// remote made absolute does: "/cwd/user:pw@host:o/r.git". Group 1 is
	// what precedes the token.
	textPassword = regexp.MustCompile("(^|[\\s'\"<>`(\\[/\\\\])[^@/\\\\:\\s'\"<>`]+:[^@/\\\\\\s'\"<>`]*@")
)

// URL returns raw with every credential removed, or Unparsed:
//
//   - A URL with a scheme loses its userinfo, query and fragment:
//     "https://user:TOKEN@github.com/o/r.git?access_token=X" becomes
//     "https://github.com/o/r.git". ssh://, git+ssh:// and every other scheme
//     are treated the same way. One that does not parse becomes Unparsed.
//   - The scp form "git@host:path" carries no password and is kept as git
//     printed it, as is a local path or "host:path"; a leading
//     "user:password@" is removed first. Any other input that contains an @
//     becomes Unparsed, because it may be a URL with a malformed scheme
//     ("https:/user:TOKEN@host", "https//user:TOKEN@host").
//   - Any input with a control character, or that is not valid UTF-8,
//     becomes Unparsed.
//
// The empty string, meaning no remote, stays empty.
func URL(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 || !utf8.ValidString(raw) {
		return Unparsed
	}
	if !schemeURL.MatchString(raw) {
		s := scpPassword.ReplaceAllString(raw, "")
		if !strings.Contains(s, "@") || scpRemote.MatchString(s) {
			return s
		}
		return Unparsed
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Unparsed
	}
	u.User = nil
	// A remote never needs a query or a fragment, and a query can carry a
	// token (?access_token=...).
	u.RawQuery, u.ForceQuery = "", false
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

// Scrub removes credentials from free text and leaves everything else as it
// was: URL is applied to every URL with a scheme, any remaining
// "scheme://user:pass@" is stripped, and so are the userinfo, the query and
// the fragment of a URL whose "//" a path cleaner collapsed
// ("/cwd/https:/user:pass@host/r.git?access_token=X"), and a
// "user:password@" prefix on a token without a scheme or on a segment of a
// path. Use it where the text must otherwise stay intact, such as the
// arguments of a domain.GitError; Message also normalises and truncates.
func Scrub(s string) string {
	s = userinfo.ReplaceAllString(s, "$1")
	s = collapsedUserinfo.ReplaceAllString(s, "$1")
	s = collapsedQuery.ReplaceAllString(s, "${1}${2}")
	s = urlToken.ReplaceAllStringFunc(s, URL)
	return textPassword.ReplaceAllString(s, "$1")
}

// Message prepares free text, typically git's stderr, for storage: Scrub
// removes credentials, control characters and runs of whitespace collapse to
// single spaces, and the result is truncated to 180 runes, ending in "…"
// when something was cut. Scrubbing happens before truncation, so a
// credential is never cut into a fragment the scrubber no longer
// recognises, and the cut never separates a combining mark from the
// character it belongs to.
func Message(s string) string {
	s = Scrub(strings.ToValidUTF8(s, "�"))
	s = strings.Join(strings.Fields(strings.Map(controlToSpace, s)), " ")
	if utf8.RuneCountInString(s) <= maxMessage {
		return s
	}
	runes := []rune(s)
	cut := maxMessage - 1
	// runes[cut] is the first rune dropped. While it is a combining mark,
	// the rune before it is the character it modifies: drop that too,
	// rather than keep a character without its accent.
	for cut > 0 && unicode.In(runes[cut], unicode.Mn, unicode.Me, unicode.Mc) {
		cut--
	}
	return string(runes[:cut]) + "…"
}

// PathMessage is Message for a path: the same scrub and one line, but a
// path longer than 180 runes keeps its end, beginning "…", since what is
// wrong with a path is most often its last segments, which a cut at the end
// would drop. The cut never leaves a combining mark without the character
// it belongs to.
func PathMessage(s string) string {
	s = Scrub(strings.ToValidUTF8(s, "�"))
	s = strings.Join(strings.Fields(strings.Map(controlToSpace, s)), " ")
	if utf8.RuneCountInString(s) <= maxMessage {
		return s
	}
	runes := []rune(s)
	from := len(runes) - (maxMessage - 1)
	// runes[from] is the first rune kept. While it is a combining mark, the
	// character it modifies was cut: drop the mark too.
	for from < len(runes) && unicode.In(runes[from], unicode.Mn, unicode.Me, unicode.Mc) {
		from++
	}
	return "…" + string(runes[from:])
}

func controlToSpace(r rune) rune {
	if unicode.IsControl(r) {
		return ' '
	}
	return r
}
