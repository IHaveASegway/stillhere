package policy_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// userSections is a complete user policy, one required table per entry, so
// a test can leave any one out.
func userSections() []struct{ table, text string } {
	return []struct{ table, text string }{
		{"", "version = 1\n"},
		{"headline", "[headline]\nmetric = \"prod_nonblank_lines\"\n"},
		{"week", "[week]\ndays = 7\nsort = \"lines_added\"\n"},
		{"blame", "[blame]\nmax_bytes = 400000\n"},
		{"bots", "[bots]\nfragments = [\"[bot]\", \"Dependabot\"]\n"},
		{"ai", "[ai]\nversion = 1\ncoauthor_emails = [\"NoReply@Anthropic.com\"]\nagent_authors = [\"copilot\"]\n"},
		{"roles", "[roles.extensions]\nprod = [\".py\", \".GO\"]\ndoc = [\".md\"]\n" +
			"[roles.names]\niac = [\"Dockerfile\"]\n" +
			"[[roles.path]]\nrole = \"test\"\npattern = '(^|/)tests(/|$)'\n" +
			"[[roles.path]]\nrole = \"seed\"\npattern = '(^|/)seeds(/|$)'\nwhen = [\"prod\"]\n"},
		{"paths", "[paths]\ndeny = ['(^|/)vendor(/|$)', '\\.min\\.js$']\n"},
	}
}

// userTOML joins the user policy without the table skip ("" keeps all).
func userTOML(skip string) string {
	var b strings.Builder
	for _, s := range userSections() {
		if skip == "" || s.table != skip {
			b.WriteString(s.text)
		}
	}
	return b.String()
}

// source frames TOML and deny list text as codebase.md documents
// Policy.Source: the TOML's byte length in decimal, a newline, the TOML,
// the deny list text.
func source(tomlText, denyText string) []byte {
	return fmt.Appendf(nil, "%d\n%s%s", len(tomlText), tomlText, denyText)
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readFile reads one of the packaged files from the package directory.
func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// usageError fails the test unless err is a *domain.UsageError, which the
// command line exits 2 for, whose message holds every one of want.
func usageError(t *testing.T, what string, err error, want ...string) {
	t.Helper()
	var usage *domain.UsageError
	if !errors.As(err, &usage) {
		t.Errorf("%s: err = %v (%T), want a *domain.UsageError", what, err, err)
		return
	}
	for _, w := range want {
		if !strings.Contains(usage.Msg, w) {
			t.Errorf("%s: message %q does not name %q", what, usage.Msg, w)
		}
	}
}

// summary renders every field of p but Name and the hash, so two policies
// can be compared field by field. fmt prints map keys sorted.
func summary(p *policy.Policy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "version=%d headline=%+v week=%+v blame=%+v bots=%q ai=%d %q %q\n",
		p.Version, p.Headline, p.Week, p.Blame, p.Bots.Fragments, p.AI.Version, p.AI.CoauthorEmails, p.AI.AgentAuthors)
	fmt.Fprintf(&b, "extensions=%v names=%v\n", p.Roles.Extensions, p.Roles.Names)
	for _, r := range p.Roles.Path {
		fmt.Fprintf(&b, "rule %s %s %v\n", r.Role, r.Pattern, r.When)
	}
	fmt.Fprintf(&b, "paths=%s\n", p.Paths.Source)
	for _, re := range p.Paths.Deny {
		fmt.Fprintf(&b, "deny %s\n", re)
	}
	return b.String()
}

// examples returns strings that n matches, or nearly: the caller keeps
// those the pattern really matches. A concatenation takes every part's
// first example, then each other example of one part at a time, so every
// alternative of every alternation appears in some string.
func examples(n *syntax.Regexp) []string {
	switch n.Op {
	case syntax.OpLiteral:
		return []string{string(n.Rune)}
	case syntax.OpCharClass:
		if len(n.Rune) == 0 {
			return nil
		}
		return []string{string(n.Rune[0]), string(n.Rune[len(n.Rune)-1])}
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return []string{"x"}
	case syntax.OpCapture, syntax.OpPlus:
		return examples(n.Sub[0])
	case syntax.OpStar, syntax.OpQuest:
		return append([]string{""}, examples(n.Sub[0])...)
	case syntax.OpRepeat:
		var out []string
		for _, e := range examples(n.Sub[0]) {
			out = append(out, strings.Repeat(e, max(n.Min, 1)))
		}
		if n.Min == 0 {
			out = append(out, "")
		}
		return out
	case syntax.OpAlternate:
		var out []string
		for _, sub := range n.Sub {
			out = append(out, examples(sub)...)
		}
		return out
	case syntax.OpConcat:
		parts := make([][]string, len(n.Sub))
		for i, sub := range n.Sub {
			if parts[i] = examples(sub); len(parts[i]) == 0 {
				return nil
			}
		}
		join := func(i int, alt string) string {
			var b strings.Builder
			for j, ex := range parts {
				if j == i {
					b.WriteString(alt)
				} else {
					b.WriteString(ex[0])
				}
			}
			return b.String()
		}
		out := []string{join(-1, "")}
		for i, ex := range parts {
			for _, alt := range ex[1:] {
				out = append(out, join(i, alt))
			}
		}
		return out
	}
	return []string{""} // anchors and empty matches
}

// TestPolicy covers the policy loader (docs/spec/classification.md#deny-list,
// docs/spec/codebase.md#run-model): the packaged policy and its deny list,
// required tables and keys (exit 2), what the hash covers, and FromSource.
func TestPolicy(t *testing.T) {
	defaultTOML := readFile(t, "default.toml")
	denyFile := readFile(t, "generated_paths.txt")

	t.Run("packaged policy", func(t *testing.T) {
		p, err := policy.Default()
		if err != nil {
			t.Fatalf("Default: %v", err)
		}
		got := summary(p)
		for _, want := range []string{
			`version=1 headline={Metric:prod_nonblank_lines} week={Days:7 Sort:lines_added} blame={MaxBytes:400000}`,
			`bots=["[bot]" "copilot" "dependabot" "renovate" "github-actions" "gpt-engineer"]`,
			`ai=1 ["noreply@anthropic.com" "aider@aider.chat"] ["copilot"]`,
			`names=map[CMakeLists.txt:iac Containerfile:iac Dockerfile:iac Makefile:iac Procfile:iac]`,
			"rule test (^|/)(tests|test|__tests__|spec|testing)(/|$) [prod sql markup iac]\n",
			"rule migration (^|/)migrations(/|$) [prod]\n",
			"rule seed (^|/)(seeders|factories|seeds|fixtures)(/|$) [prod sql]\n",
			"paths=package:generated_paths.txt\n",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("packaged policy lacks %q:\n%s", want, got)
			}
		}
		if p.Name != "package:default.toml" {
			t.Errorf("Name = %q", p.Name)
		}
		// The hash covers the TOML bytes, a length prefix and the deny list.
		if want := source(defaultTOML, denyFile); string(p.Source) != string(want) {
			t.Errorf("Source is not the length-prefixed default.toml and generated_paths.txt")
		}
		// Those bytes are the checkout's: a CRLF checkout (core.autocrlf on
		// Windows) would build a second packaged hash from the same commit,
		// which the package's .gitattributes (eol=lf) prevents.
		if strings.Contains(defaultTOML+denyFile, "\r") {
			t.Errorf("default.toml or generated_paths.txt holds a CR: check internal/policy/.gitattributes")
		}
		if p.SHA256 != sha(p.Source) || p.Ref() != (domain.PolicyRef{Version: 1, SHA256: sha(p.Source)}) {
			t.Errorf("SHA256 = %s, Ref = %+v; want the SHA-256 of Source, %s", p.SHA256, p.Ref(), sha(p.Source))
		}
	})

	t.Run("deny list file", func(t *testing.T) {
		// Every pattern line compiles as RE2: a lookahead, which Linguist's
		// Ruby allows and RE2 lacks, would fail here and in Default.
		patterns := 0
		var header []string
		for i, line := range strings.Split(denyFile, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") && patterns == 0 {
				header = append(header, strings.TrimSpace(strings.TrimPrefix(line, "#")))
			}
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			patterns++
			if _, err := regexp.Compile(line); err != nil {
				t.Errorf("generated_paths.txt line %d: %v", i+1, err)
			}
		}
		if patterns < 200 {
			t.Errorf("generated_paths.txt has %d patterns; the Linguist translation alone has more than 200", patterns)
		}
		// The header names RE2, Linguist and its license, and the upstream
		// commit the curator read.
		head := strings.Join(header, " ")
		for _, want := range []string{"RE2", "GitHub Linguist", "github.com/github-linguist/linguist", "Copyright (c) 2017 GitHub, Inc.", "Permission is hereby granted"} {
			if !strings.Contains(head, want) {
				t.Errorf("generated_paths.txt header does not name %q", want)
			}
		}
		if !regexp.MustCompile(`commit\s+[0-9a-f]{40}\b`).MatchString(head) {
			t.Errorf("generated_paths.txt header records no upstream commit")
		}
		_, err := policy.FromSource("lookahead", source(defaultTOML, "(?=vendor/)\n"))
		if err == nil || !strings.Contains(err.Error(), "generated_paths.txt line 1") {
			t.Errorf("a lookahead in the deny list: err = %v, want a failure naming the line", err)
		}

		// The minimum contents of classification.md#deny-list.
		p, err := policy.Default()
		if err != nil {
			t.Fatalf("Default: %v", err)
		}
		for _, path := range []string{
			"node_modules/x/index.js", "vendor/lib.py", "dist/app.js", "coverage/lcov.info",
			".next/cache.js", "out/main.js", "third_party/zlib/zlib.h", "pkg/__pycache__/m.py", ".venv/lib/x.py",
			"venv/lib/x.py", "storage/framework/views/0a1b.php", "apps/web/storage/logs/laravel.log",
			"bootstrap/cache/config.php", "go.sum", "web/app.min.js",
			"api/user.pb.go", "api/user_pb2.py", "internal/mocks/store.go", "gen/api.go", "GPBMetadata/User.php",
			"package-lock.json", "yarn.lock", "Cargo.lock", "composer.lock", "Gemfile.lock", "poetry.lock",
		} {
			if !p.Paths.Denies(path) {
				t.Errorf("the packaged deny list does not deny %s", path)
			}
		}
		// Left out on purpose: directories that usually hold a team's own
		// code (Linguist's cache/ and env/; build/ and storage/ beyond
		// Laravel's), and names that only contain a denied segment.
		for _, path := range []string{
			"api/user.proto", "service.py", "internal/cache/lru.go", "internal/env/env.go",
			"internal/storage/postgres.go", "storage/filesystem/dotgit/dotgit.go", "src/go/build/build.go",
			"build/ci/release.sh", "builder/x.go", "vendored.go",
		} {
			if p.Paths.Denies(path) {
				t.Errorf("the packaged deny list denies %s", path)
			}
		}
	})

	t.Run("user file", func(t *testing.T) {
		text := userTOML("")
		p, err := policy.Parse("stillhere.toml", []byte(text))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		got := summary(p)
		for _, want := range []string{
			`bots=["[bot]" "dependabot"]`, // fragments are lowercased
			`ai=1 ["noreply@anthropic.com"] ["copilot"]`,
			`extensions=map[.go:prod .md:doc .py:prod]`, // extensions are lowercased
			"rule seed (^|/)seeds(/|$) [prod]\n",
			"paths=policy\ndeny (^|/)vendor(/|$)\ndeny \\.min\\.js$\n",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("parsed policy lacks %q:\n%s", want, got)
			}
		}
		if p.Name != "stillhere.toml" || string(p.Source) != string(source(text, "")) || p.SHA256 != sha(p.Source) {
			t.Errorf("Name %q, Source %q, SHA256 %s: want the length-prefixed file and its hash", p.Name, p.Source, p.SHA256)
		}
	})

	t.Run("missing table exits 2", func(t *testing.T) {
		for _, table := range []string{"headline", "week", "blame", "bots", "ai", "roles", "paths"} {
			_, err := policy.Parse("stillhere.toml", []byte(userTOML(table)))
			usageError(t, "without ["+table+"]", err, "stillhere.toml", "["+table+"]")
		}
		_, err := policy.Parse("stillhere.toml", []byte(userTOML("")[len("version = 1\n"):]))
		usageError(t, "without version", err, "missing key version")
	})

	t.Run("missing key exits 2", func(t *testing.T) {
		full := userTOML("")
		for _, c := range []struct{ old, new, want string }{
			{"metric = \"prod_nonblank_lines\"\n", "", "[headline] metric"},
			{"days = 7\n", "", "[week] days"},
			{"sort = \"lines_added\"\n", "", "[week] sort"},
			{"max_bytes = 400000\n", "", "[blame] max_bytes"},
			{"fragments = [\"[bot]\", \"Dependabot\"]\n", "", "[bots] fragments"},
			{"[ai]\nversion = 1\n", "[ai]\n", "[ai] version"},
			{"coauthor_emails = [\"NoReply@Anthropic.com\"]\n", "", "[ai] coauthor_emails"},
			{"agent_authors = [\"copilot\"]\n", "", "[ai] agent_authors"},
			{"[roles.names]\niac = [\"Dockerfile\"]\n", "", "[roles] names"},
			{"deny = ['(^|/)vendor(/|$)', '\\.min\\.js$']\n", "", "[paths] deny"},
		} {
			text := strings.Replace(full, c.old, c.new, 1)
			if text == full {
				t.Fatalf("test bug: %q is not in the policy", c.old)
			}
			_, err := policy.Parse("stillhere.toml", []byte(text))
			usageError(t, "without "+c.want, err, "missing key "+c.want)
		}
	})

	t.Run("deny or package file", func(t *testing.T) {
		withPaths := func(paths string) []byte { return []byte(userTOML("paths") + "[paths]\n" + paths) }
		_, err := policy.Parse("stillhere.toml", withPaths("package_file = \"generated_paths.txt\"\n"))
		usageError(t, "package_file", err, "package_file", "[paths] deny")
		_, err = policy.Parse("stillhere.toml", withPaths("package_file = \"generated_paths.txt\"\ndeny = []\n"))
		usageError(t, "both", err, "both deny and package_file")
		_, err = policy.Parse("stillhere.toml", withPaths(""))
		usageError(t, "neither", err, "missing key [paths] deny")
		_, err = policy.Parse("default.toml", []byte(defaultTOML))
		usageError(t, "the packaged file as a user file", err, "package_file")
		p, err := policy.Parse("stillhere.toml", withPaths("deny = []\n"))
		if err != nil || p.Paths.Deny != nil || p.Paths.Denies("vendor/x") {
			t.Errorf("an empty deny list: %v, %v", p, err)
		}
	})

	t.Run("bad values exit 2", func(t *testing.T) {
		full := userTOML("")
		for _, c := range []struct{ old, new, want string }{
			{"'(^|/)vendor(/|$)'", "'(?=vendor/)'", "[paths] deny pattern 1"},
			{"'(^|/)vendor(/|$)'", "''", "empty pattern"},
			{"pattern = '(^|/)tests(/|$)'", "pattern = '(?<!x)tests'", "[[roles.path]] rule 1"},
			{"role = \"test\"", "role = \"tests\"", "role \"tests\" is not a role"},
			{"when = [\"prod\"]", "when = [\"prod\", \"production\"]", "when \"production\" is not a role"},
			{"days = 7", "Days = 7", "unknown key [week] Days"},
			{"[blame]", "[hotspots]\nsince_days = 365\n[blame]", "unknown table [hotspots]"},
			{"doc = [\".md\"]", "docs = [\".md\"]", "[roles.extensions] docs: not a role"},
			{"version = 1\n[headline]", "version = 2\n[headline]", "version = 2"},
			{"metric = \"prod_nonblank_lines\"", "metric = \"geared_fp\"", "[headline] metric"},
			{"days = 7", "days = 0", "[week] days = 0"},
			// A day count that window arithmetic would overflow.
			{"days = 7", "days = 9223372036854775807", "[week] days = 9223372036854775807: want 1 to 36500"},
			{"days = 7", "days = \"7\"", "week.days"},
			{"sort = \"lines_added\"", "sort = \"lines\"", "[week] sort"},
			{"max_bytes = 400000", "max_bytes = 0", "[blame] max_bytes"},
			{"\"Dependabot\"", "\" \"", "blank fragment"},
			// Co-author rules match whole emails, never a loose "noreply".
			{"\"NoReply@Anthropic.com\"", "\"noreply\"", `"noreply" is not an email address`},
			{"doc = [\".md\"]", "doc = [\".md\", \".py\"]", "listed for both"},
			{"doc = [\".md\"]", "doc = [\"md\"]", "want a dot"},
			// classify reads only the last dot suffix, so this would never match.
			{"doc = [\".md\"]", "doc = [\".md\", \".tar.gz\"]", "last dot suffix"},
			{"iac = [\"Dockerfile\"]", "iac = [\"build/Dockerfile\"]", "without a slash"},
		} {
			text := strings.Replace(full, c.old, c.new, 1)
			if text == full {
				t.Fatalf("test bug: %q is not in the policy", c.old)
			}
			_, err := policy.Parse("stillhere.toml", []byte(text))
			usageError(t, c.new, err, "stillhere.toml: ", c.want)
		}
	})

	// --days and --week-sort override [week] for one run, outside the
	// policy (app.Week; the window half is TestWeekOracle's): the policy
	// holds the file's defaults, and its hash is the file's bytes, which
	// [week] days is part of and no flag is.
	t.Run("days are the file's, and in its hash", func(t *testing.T) {
		seven, err := policy.Parse("a.toml", []byte(userTOML("")))
		if err != nil {
			t.Fatal(err)
		}
		again, err := policy.Parse("b.toml", []byte(userTOML("")))
		if err != nil {
			t.Fatal(err)
		}
		one, err := policy.Parse("a.toml", []byte(strings.Replace(userTOML(""), "days = 7", "days = 1", 1)))
		if err != nil {
			t.Fatal(err)
		}
		if seven.Week.Days != 7 || one.Week.Days != 1 {
			t.Errorf("[week] days read as %d and %d, want 7 and 1", seven.Week.Days, one.Week.Days)
		}
		if seven.Ref() != again.Ref() {
			t.Errorf("the same bytes under two names hash differently: %v, %v", seven.Ref(), again.Ref())
		}
		if seven.Ref() == one.Ref() {
			t.Errorf("[week] days is not covered by the hash")
		}
	})

	t.Run("the hash covers the deny list", func(t *testing.T) {
		p, err := policy.Default()
		if err != nil {
			t.Fatal(err)
		}
		// A stored source rebuilds its own deny list, not today's packaged
		// one: a newer default never changes a stored run's policy.
		edited, err := policy.FromSource("run 7", source(defaultTOML, denyFile+"(^|/)only_in_run_7(/|$)\n"))
		if err != nil {
			t.Fatalf("FromSource: %v", err)
		}
		if edited.SHA256 == p.SHA256 || !edited.Paths.Denies("only_in_run_7/x.py") || p.Paths.Denies("only_in_run_7/x.py") {
			t.Errorf("an added deny pattern: hash %s vs %s, run 7 denies %v, today denies %v",
				edited.SHA256, p.SHA256, edited.Paths.Denies("only_in_run_7/x.py"), p.Paths.Denies("only_in_run_7/x.py"))
		}
		// The length prefix: moving the deny list's first byte, a #, into
		// the TOML keeps the concatenated bytes and turns the file's first
		// comment into a pattern. Without the prefix both would hash alike.
		moved, err := policy.FromSource("moved", source(defaultTOML+denyFile[:1], denyFile[1:]))
		if err != nil {
			t.Fatalf("FromSource: %v", err)
		}
		if defaultTOML+denyFile != defaultTOML+denyFile[:1]+denyFile[1:] || moved.SHA256 == p.SHA256 || len(moved.Paths.Deny) != len(p.Paths.Deny)+1 {
			t.Errorf("a byte moved across the boundary: hash %s vs %s, %d patterns vs %d",
				moved.SHA256, p.SHA256, len(moved.Paths.Deny), len(p.Paths.Deny))
		}
	})

	t.Run("FromSource has the same Ref", func(t *testing.T) {
		packaged, err := policy.Default()
		if err != nil {
			t.Fatal(err)
		}
		user, err := policy.Parse("stillhere.toml", []byte(userTOML("")))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []*policy.Policy{packaged, user} {
			got, err := policy.FromSource("run 12", p.Source)
			if err != nil {
				t.Fatalf("FromSource(%s): %v", p.Name, err)
			}
			if got.Ref() != p.Ref() || got.Name != "run 12" || string(got.Source) != string(p.Source) || summary(got) != summary(p) {
				t.Errorf("FromSource(%s) = %v %q, want %v and the same fields:\n%s\nvs\n%s",
					p.Name, got.Ref(), got.Name, p.Ref(), summary(got), summary(p))
			}
		}
	})

	t.Run("a corrupt source is not a usage error", func(t *testing.T) {
		toml := userTOML("")
		for _, src := range [][]byte{
			nil,
			[]byte("x"),
			[]byte("+1\nx"),
			[]byte("-1\nx"),
			[]byte("01\nx"),
			[]byte("9\nshort"),
			[]byte(fmt.Sprint(len(toml), "\n", toml, "(^|/)extra/\n")), // a deny key and deny list text
			[]byte("4\n[x]\n"),
		} {
			_, err := policy.FromSource("run 3", src)
			var usage *domain.UsageError
			if err == nil || errors.As(err, &usage) || !strings.HasPrefix(err.Error(), "run 3: ") {
				t.Errorf("FromSource(%q) = %v, want a plain error naming the run", src, err)
			}
		}
	})

	// Denies skips a pattern when the path holds none of its literals, which
	// must never change an answer. It can only go wrong one way, by skipping
	// a pattern that matches, so every pattern is checked on strings it
	// matches, made from its own syntax with each alternative taken in turn,
	// and in upper case and with the long s that (?i) folds to s.
	t.Run("the deny prefilter never changes an answer", func(t *testing.T) {
		p, err := policy.Default()
		if err != nil {
			t.Fatal(err)
		}
		folded, err := policy.Parse("folded.toml", []byte(strings.Replace(userTOML(""),
			`deny = ['(^|/)vendor(/|$)', '\.min\.js$']`,
			`deny = ['(?i)ſtorage/', '(?i)\.designer\.(cs|vb)$', '(a|b)+c{2,}d', 'x\x{FFFD}y', '^$']`, 1)))
		if err != nil {
			t.Fatal(err)
		}
		// (?i) also folds s with the long s and k with the Kelvin sign.
		folds := strings.NewReplacer("s", "ſ", "S", "ſ", "k", "K", "K", "K")
		checked := 0
		for _, pol := range []*policy.Policy{p, folded} {
			for _, re := range pol.Paths.Deny {
				tree, err := syntax.Parse(re.String(), syntax.Perl)
				if err != nil {
					t.Fatal(err)
				}
				for _, ex := range examples(tree.Simplify()) {
					for _, path := range []string{ex, strings.ToUpper(ex), strings.ToLower(ex), folds.Replace(ex), "dir/" + ex} {
						if !re.MatchString(path) {
							continue
						}
						checked++
						if !pol.Paths.Denies(path) {
							t.Errorf("%s: Denies(%q) = false, but %s matches it", pol.Name, path, re)
						}
					}
				}
			}
		}
		if checked < 1000 {
			t.Errorf("only %d strings checked; the example generator lost its way", checked)
		}
		// A pattern added by hand has no prefilter and is always tried.
		hand := policy.Paths{Deny: []*regexp.Regexp{regexp.MustCompile(`(?i)ſ`)}}
		if !hand.Denies("S") {
			t.Errorf("a hand-built deny list skipped its pattern")
		}
	})
}
