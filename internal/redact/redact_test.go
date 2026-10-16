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
			// A URL given where a path is expected, after filepath.Abs
			// collapsed its "//": what a PATH argument becomes.
			"/tmp/run/https:/x-access-token:TOKEN@github.com/o/r.git: does not exist",
			"/tmp/run/https:/TOKEN@github.com/o/r.git: does not exist",
			`C:\run\https:\user:TOKEN@github.com\o\r.git: does not exist`,
			"stat /tmp/run/user:TOKEN@host.example:o/r.git: not a directory",
			// The query and the fragment of such a URL, which URL removes
			// from the URL as typed, with or without a host before them.
			"/tmp/run/https:/github.com/o/r.git?access_token=TOKEN: does not exist",
			"/tmp/run/https:/github.com/o/r.git#TOKEN: does not exist",
			"/tmp/run/https:/?access_token=TOKEN: does not exist",
			`C:\run\https:\github.com\o\r.git?access_token=TOKEN: does not exist`,
			"stat '/tmp/run/https:/user:TOKEN@github.com/o/r.git?access_token=TOKEN': no such file",
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
			"/home/ada/go/pkg/mod/golang.org/x/sync@v0.8.0/errgroup",
			"git@github.com:/o/r.git",
			"C:/src/r and HEAD@{1}",
			// A drive letter is no scheme, so a "#" in a Windows path is no
			// fragment.
			`C:\src\C#\app: does not exist`,
			"C:/src/C#/app: does not exist",
			"/home/ada/src/C#/app: does not exist",
		} {
			if got := redact.Scrub(in); got != in {
				t.Errorf("Scrub(%q) = %q, want it unchanged", in, got)
			}
		}
		for _, tc := range []struct{ in, want string }{
			{
				"clone https://user:TOKEN@github.com/o/" + strings.Repeat("x", 300) + "  tail",
				"clone https://github.com/o/" + strings.Repeat("x", 300) + "  tail",
			},
			// A URL made absolute loses its query or fragment and keeps the
			// rest, the ":" that joins a path to its message included.
			{
				"/tmp/run/https:/github.com/o/r.git?access_token=TOKEN: does not exist",
				"/tmp/run/https:/github.com/o/r.git: does not exist",
			},
			{"'/tmp/run/https:/github.com/o/r.git#TOKEN' and more", "'/tmp/run/https:/github.com/o/r.git' and more"},
		} {
			if got := redact.Scrub(tc.in); got != tc.want {
				t.Errorf("Scrub(%q) = %q, want %q", tc.in, got, tc.want)
			}
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
		// A path keeps its end, where what is wrong with it most often is: a
		// URL pasted as a PATH under a deep working directory (#93).
		path := "/" + strings.Repeat("deep/", 40) + "https:/user:TOKEN@example.com/acme/pay.git"
		got := redact.PathMessage(path)
		if n := utf8.RuneCountInString(got); n > 180 || !strings.HasPrefix(got, "…") ||
			!strings.HasSuffix(got, "/https:/example.com/acme/pay.git") || strings.Contains(got, "TOK") {
			t.Errorf("PathMessage(a %d-rune path) = %d runes %q, want at most 180 beginning … and ending with the scrubbed URL",
				utf8.RuneCountInString(path), n, got)
		}
		for _, unit := range []string{"é", "日", "e\u0301"} {
			got := redact.PathMessage(strings.Repeat(unit, 200))
			if body := strings.TrimPrefix(got, "…"); strings.Count(body, unit)*len(unit) != len(body) || !utf8.ValidString(got) {
				t.Errorf("PathMessage(200 × %q) cut inside a character: %q", unit, got)
			}
		}
	})
}
