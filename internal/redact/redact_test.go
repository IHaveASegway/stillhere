package redact_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IHaveASegway/stillhere/internal/redact"
)

// TestRedact is the contract test of the scrubber
// (docs/spec/git-invocation.md#url-scrubbing, docs/spec/codebase.md#tests):
// no credential survives URL, Scrub or Message, and what is not a credential
// survives unchanged.
func TestRedact(t *testing.T) {
	t.Run("URL", func(t *testing.T) {
		for _, tc := range []struct{ in, want string }{
			{"", ""},
			{"https://github.com/o/r.git", "https://github.com/o/r.git"},
			{"https://user:TOKEN@github.com/o/r.git", "https://github.com/o/r.git"},
			{"https://x-access-token:TOKEN@github.com/o/r.git", "https://github.com/o/r.git"},
			{"https://TOKEN@github.com/o/r.git", "https://github.com/o/r.git"},
			{"ssh://user:TOKEN@host.example/o/r.git", "ssh://host.example/o/r.git"},
			{"git+ssh://git@host.example:22/o/r.git", "git+ssh://host.example:22/o/r.git"},
			{"https://[::1]:8443/o/r.git", "https://[::1]:8443/o/r.git"},
			{"file:///srv/git/r.git", "file:///srv/git/r.git"},
			// A query or fragment can carry a token; a remote needs neither.
			{"https://github.com/o/r.git?access_token=TOKEN", "https://github.com/o/r.git"},
			{"https://host.example/o/r.git#TOKEN", "https://host.example/o/r.git"},
			// scp-like remotes and local paths.
			{"git@github.com:o/r.git", "git@github.com:o/r.git"},
			{"github.com:o/r.git", "github.com:o/r.git"},
			{"user:TOKEN@host.example:o/r.git", "host.example:o/r.git"},
			{"/srv/git/r.git", "/srv/git/r.git"},
			{"../r.git", "../r.git"},
			{`C:\src\r`, `C:\src\r`},
			// Malformed schemes are not scp-like remotes, so they are not
			// kept as git printed them.
			{"https:/user:TOKEN@host.example/x", redact.Unparsed},
			{"https//user:TOKEN@host.example/x", redact.Unparsed},
			{"http//user:TOKEN@host.example", redact.Unparsed},
			{"/srv/git/user:TOKEN@r.git", redact.Unparsed},
			// Unparsable input never comes back raw.
			{"https://user:TOKEN@host.example/%zz", redact.Unparsed},
			{"https://host.example/a b\x00TOKEN", redact.Unparsed},
			{"https://host.example/\xffTOKEN", redact.Unparsed},
		} {
			if got := redact.URL(tc.in); got != tc.want {
				t.Errorf("URL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})

	t.Run("Scrub and Message remove credentials from free text", func(t *testing.T) {
		for _, in := range []string{
			"fatal: unable to access 'https://user:TOKEN@github.com/o/r.git/': The requested URL returned error: 403",
			`fatal: unable to access "https://x-access-token:TOKEN@github.com/o/r.git/"`,
			"see <https://user:TOKEN@host.example/x>",
			"https://user:pa'ssTOKEN@host.example/x",
			"https://user:p@ssTOKEN@host.example/x",
			"remote: ssh://deploy:TOKEN@git.example.com/x and more",
			"fatal: 'user:TOKEN@host.example:o/r.git' does not appear to be a git repository",
			"fatal: user:TOKEN@host.example:o/r.git: not found",
			"see https://host.example/o/r.git?access_token=TOKEN for details",
			"\x1b[31mhttps://user:TOKEN@host.example/x\x1b[0m",
		} {
			for name, scrub := range map[string]func(string) string{"Scrub": redact.Scrub, "Message": redact.Message} {
				if got := scrub(in); strings.Contains(got, "TOKEN") {
					t.Errorf("%s(%q) = %q, still holds the credential", name, in, got)
				}
			}
		}
	})

	t.Run("Scrub changes nothing but credentials", func(t *testing.T) {
		for _, in := range []string{
			"log --format=%H%x00%aE -- path with  two spaces",
			"Ada Example <ada@example.com>",
			"refs/heads/*:refs/heads/*",
			"git@github.com:o/r.git",
			"\xff\xfe raw bytes in a path",
		} {
			if got := redact.Scrub(in); got != in {
				t.Errorf("Scrub(%q) = %q, want it unchanged", in, got)
			}
		}
		long := "clone https://user:TOKEN@github.com/o/" + strings.Repeat("x", 300) + "  tail"
		want := "clone https://github.com/o/" + strings.Repeat("x", 300) + "  tail"
		if got := redact.Scrub(long); got != want {
			t.Errorf("Scrub(long) = %q, want %q", got, want)
		}
	})

	t.Run("Message makes one line of at most 180 runes", func(t *testing.T) {
		if got, want := redact.Message("error:\n\tfirst\r\n  second\x00third"), "error: first second third"; got != want {
			t.Errorf("Message = %q, want %q", got, want)
		}
		for _, unit := range []string{"a", "é", "日", "😀", "e\u0301"} {
			got := redact.Message(strings.Repeat(unit, 200))
			if n := utf8.RuneCountInString(got); n > 180 || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
				t.Errorf("Message(200 × %q) = %d runes %q, want at most 180 valid runes ending in …", unit, n, got)
			}
			if body := strings.TrimSuffix(got, "…"); strings.Count(body, unit)*len(unit) != len(body) {
				t.Errorf("Message(200 × %q) cut inside a character: %q", unit, got)
			}
		}
		// A credential next to the cut is removed before the cut, never
		// left as a fragment the scrubber cannot recognise.
		in := strings.Repeat("x", 170) + " https://user:TOKEN@github.com/o/r.git"
		if got := redact.Message(in); strings.Contains(got, "TOK") {
			t.Errorf("Message kept part of a credential at the cut: %q", got)
		}
	})
}
