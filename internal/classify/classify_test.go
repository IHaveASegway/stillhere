package classify_test

import (
	"strings"
	"testing"

	"github.com/IHaveASegway/stillhere/internal/classify"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// TestClassify pins the ordered classifier under the packaged policy
// (docs/spec/classification.md): roles and their precedence, what counts as
// product code (revision 3), segment matching, the deny list, too_big and a
// path rule's max_bytes, the header sniff, how .gitattributes overrides the
// deny list, and the rule Explain names for each.
func TestClassify(t *testing.T) {
	p, err := policy.Default()
	if err != nil {
		t.Fatalf("policy.Default: %v", err)
	}

	t.Run("roles", func(t *testing.T) {
		for _, c := range []struct {
			path string
			want domain.Role
		}{
			// Extensions, ignoring case, and exact names.
			{"service.py", domain.RoleProd},
			{"api/schema.proto", domain.RoleProd}, // protobuf sources stay prod
			{"db/report.SQL", domain.RoleSQL},
			{"web/site.css", domain.RoleMarkup},
			{"ios/Main.storyboard", domain.RoleMarkup},
			{"deploy/values.yaml", domain.RoleIaC},
			{"scripts/release.sh", domain.RoleIaC},
			{"Dockerfile", domain.RoleIaC},
			{"services/api/Dockerfile", domain.RoleIaC},
			{"dockerfile", ""}, // names are exact
			{"Makefile", domain.RoleIaC},
			{"README.md", domain.RoleDoc},
			{"notes.csv", ""},
			{"LICENSE", ""},

			// Test segments match whole segments. The naive check, "/tests/"
			// as a substring, misses tests/test_service.py, whose path never
			// holds those characters.
			{"tests/test_service.py", domain.RoleTest},
			{"pkg/tests/helper.go", domain.RoleTest},
			{"src/test/java/AppTest.java", domain.RoleTest},
			{"src/__tests__/button.js", domain.RoleTest},
			{"spec/models/user_spec.rb", domain.RoleTest},
			{"testsuite/runner.py", domain.RoleProd}, // not a test segment
			{"contest/entry.py", domain.RoleProd},

			// The packaged test-segment rule admits code and configuration
			// only: data, golden files and snapshots get no role, and
			// documentation stays doc, so docs/spec/ is no RSpec suite.
			{"test/e2e/values.yaml", domain.RoleTest},
			{"spec/fixtures.sql", domain.RoleTest},
			{"tests/data.csv", ""},
			{"tests/golden/report.txt", ""},
			{"src/__tests__/__snapshots__/button.test.js.snap", ""},
			{"tests/README.md", domain.RoleDoc},
			{"docs/spec/codebase.md", domain.RoleDoc},
			{"tests", ""}, // a name with no role is not admitted, even where the segment is the whole path

			// Test names.
			{"internal/rank/rank_test.go", domain.RoleTest},
			{"src/button.test.tsx", domain.RoleTest},
			{"src/button.spec.ts", domain.RoleTest},
			{"src/util.test.js", domain.RoleTest},
			{"app/UserTest.php", domain.RoleTest},
			{"app/latest.php", domain.RoleProd}, // ends in test.php, but is no PHPUnit test
			{"App/AppTests.swift", domain.RoleTest},

			// Path roles override extension roles, and a rule applies only
			// when its condition admits the extension role.
			{"db/migrations/0001_init.py", domain.RoleMigration},
			{"db/migrations/0001_init.sql", domain.RoleSQL}, // migration admits prod only
			{"db/seeds/users.py", domain.RoleSeed},
			{"database/factories/UserFactory.php", domain.RoleSeed},
			{"db/migrations/seeds/x.sql", domain.RoleSeed}, // skips migration, takes seed
			{"db/seeds/readme.md", domain.RoleDoc},         // seed admits prod and sql only

			// Precedence among path roles: test, then migration, then seed.
			// (The deny list denies tests/fixtures/ before any role, which
			// Classify applies and Role does not.)
			{"tests/fixtures/x.py", domain.RoleTest},
			{"tests/migrations/x.py", domain.RoleTest},
			{"migrations/seeds/x.py", domain.RoleMigration},

			// Revision 3, what counts as product code. Example code is doc,
			// at any depth, before migrations and seeds.
			{"examples/hello-world/src/main.rs", domain.RoleDoc},
			{"crates/grep/examples/simplegrep.rs", domain.RoleDoc},
			{"_examples/rest/main.go", domain.RoleDoc},
			{"_example/main.go", domain.RoleDoc},
			{"example/example/settings.py", domain.RoleDoc},
			{"docs/examples/cache_extension.py", domain.RoleDoc},
			{"examples/app/migrations/0001_initial.py", domain.RoleDoc},
			{"examples/templates/home.html", domain.RoleDoc},
			{"examples/testing/tests/x.rs", domain.RoleTest}, // test first
			{"samples/basic.py", domain.RoleDoc},
			{"pkg/samples/x.go", domain.RoleProd}, // samples/ and demos/ at the top only, as Linguist reads them
			// A capital first letter counts, as in Linguist's patterns:
			// SwiftPM's Examples/, .NET's Samples/ and Benchmarks/.
			{"Examples/Basic/main.swift", domain.RoleDoc},
			{"Samples/Console/Program.cs", domain.RoleDoc},
			{"Demo/app.js", domain.RoleDoc},
			{"Docs/conf.py", domain.RoleDoc},
			{"Benchmarks/Bench.cs", domain.RoleTest},
			{"Sources/Lib/lib.swift", domain.RoleProd},
			{"Docs/v1/docs-gen.go", domain.RoleProd}, // a Go package, as under docs/
			{"internal/exampleutil/x.go", domain.RoleProd},
			// Benchmarks are test: Cargo's benches/, other ecosystems'
			// directories, *_bench.* and *.bench.* names, a top-level
			// bench.py and perf/.
			{"axum/benches/benches.rs", domain.RoleTest},
			{"benchmarks/routers/src/bench.mts", domain.RoleTest},
			{"bench.py", domain.RoleTest},
			{"crates/core/flags_bench.rs", domain.RoleTest},
			{"src/router.bench.ts", domain.RoleTest},
			{"perf/runner.py", domain.RoleTest},
			{"tools/perf/builtin-record.c", domain.RoleProd}, // perf/ at the top only: Linux's tools/perf is a product
			{"src/benchmark/mod.rs", domain.RoleProd},        // a benchmarking tool's own source
			{"src/benchmark.rs", domain.RoleProd},
			{"conftest.py", domain.RoleTest},
			{"src/pkg/conftest.py", domain.RoleTest},
			// Code under the top-level docs/ is doc; a top-level doc/ is not
			// (cobra's doc/ package writes man pages).
			{"docs/conf.py", domain.RoleDoc},
			{"docs/.vitepress/theme/index.ts", domain.RoleDoc},
			{"doc/man_docs.go", domain.RoleProd},
			{"pkg/docs/x.py", domain.RoleProd},
			// Go code keeps its role where a directory is a package by that
			// name: google.golang.org/api's docs/v1 and script/v1, nats.go's
			// bench, a build-information package.
			{"docs/v1/docs-gen.go", domain.RoleProd},
			{"script/v1/script-gen.go", domain.RoleProd},
			{"bench/bench.go", domain.RoleProd},
			{"build/info.go", domain.RoleProd},
			{"bench/bench_test.go", domain.RoleTest},
			{"cmd/benchmarks/main.go", domain.RoleTest},
			// CI is iac: code under .github/ and the other CI directories;
			// GitHub's templates and FUNDING.yml are doc.
			{".github/workflows/ci.yml", domain.RoleIaC},
			{".github/scripts/release.py", domain.RoleIaC},
			{".circleci/config.yml", domain.RoleIaC},
			{".teamcity/settings.kts", domain.RoleIaC},
			{".github/ISSUE_TEMPLATE/bug.yml", domain.RoleDoc},
			{".github/pull_request_template.md", domain.RoleDoc},
			{".github/FUNDING.yml", domain.RoleDoc},
			{"Jenkinsfile", domain.RoleIaC},
			// Build and release tooling at the top, build and tool
			// configuration as code, Python packaging, a Cargo build script.
			{"build/dts-plugins.ts", domain.RoleIaC},
			{"scripts/unasync.py", domain.RoleIaC},
			{"scripts/release.go", domain.RoleIaC},
			{"script/build.ts", domain.RoleIaC},
			{"packages/vite/scripts/x.ts", domain.RoleProd}, // the top only
			{"vite.config.ts", domain.RoleIaC},
			{"adapters/bun/vite.config.ts", domain.RoleIaC},
			{"packages/vite/rolldown.dts.config.ts", domain.RoleIaC},
			{"eslint.config.mjs", domain.RoleIaC},
			{".eslintrc.js", domain.RoleIaC},
			{"gulpfile.js", domain.RoleIaC},
			{"src/app/app.config.ts", domain.RoleProd}, // an application's own config module
			{"setup.py", domain.RoleIaC},
			{"homeassistant/setup.py", domain.RoleProd}, // a module named setup
			{"noxfile.py", domain.RoleIaC},
			{"fabfile.py", domain.RoleIaC}, // the deny list leaves Linguist's Fabric rule to this one
			{"setup.cfg", domain.RoleIaC},
			{"tox.ini", domain.RoleIaC},
			{"MANIFEST.in", domain.RoleIaC},
			{"requirements.txt", domain.RoleIaC},
			{"docs/requirements-dev.txt", domain.RoleIaC},
			{"requirements/test.in", domain.RoleIaC},
			{"build.rs", domain.RoleIaC},
			{"crates/core/build.rs", domain.RoleIaC},
			{"src/bin/cargo/commands/build.rs", domain.RoleProd}, // a module
			{"srcs/build.rs", domain.RoleIaC},
			// Jinja templates are markup.
			{"templates/layout.jinja", domain.RoleMarkup},
			{"semconv/templates/metric.go.j2", domain.RoleMarkup},
		} {
			if got := classify.Role(p, c.path); got != c.want {
				t.Errorf("Role(%q) = %q, want %q", c.path, got, c.want)
			}
		}
		// The paths only a path rule gives a role, which the header sniff
		// must name one by one: no extension or name gives them one.
		for path, want := range map[string]bool{
			"requirements.txt": true, "requirements/dev.in": true,
			"setup.py": false, "tests/x.py": false, "notes.txt": false,
		} {
			if got := classify.RoleByRuleAlone(p, path); got != want {
				t.Errorf("RoleByRuleAlone(%q) = %v, want %v", path, got, want)
			}
		}
	})

	t.Run("order", func(t *testing.T) {
		at := func(path string, size int64) classify.Input {
			return classify.Input{Path: path, AtHead: true, Size: size, Generated: "unspecified", Vendored: "unspecified"}
		}
		withHeader := func(in classify.Input) classify.Input { in.HeaderHit = true; return in }
		gone := func(in classify.Input) classify.Input { in.AtHead = false; return in }
		notFile := func(in classify.Input) classify.Input { in.NotFile = true; return in }
		for _, c := range []struct {
			name string
			in   classify.Input
			role domain.Role
			skip domain.Skip
		}{
			{"a role", at("service.py", 250), domain.RoleProd, ""},
			{"no role", at("notes.csv", 68), "", domain.SkipUnclassified},

			// The deny list: segments, two consecutive segments, lock and sum
			// files, minified and generated files.
			{"vendor segment", at("vendor/lib.py", 172), "", domain.SkipDeniedPath},
			{"nested vendor segment", at("services/api/vendor/x/y.go", 10), "", domain.SkipDeniedPath},
			{"bootstrap/cache", at("bootstrap/cache/config.php", 40), "", domain.SkipDeniedPath},
			{"bootstrap/cache, nested", at("apps/web/bootstrap/cache/services.php", 40), "", domain.SkipDeniedPath},
			{"not bootstrap/cache", at("bootstrap/cachex/config.php", 40), domain.RoleProd, ""},
			{"go.sum", at("go.sum", 229), "", domain.SkipDeniedPath},
			{"node_modules", at("node_modules/left-pad/index.js", 10), "", domain.SkipDeniedPath},
			{"minified", at("web/app.min.js", 10), "", domain.SkipDeniedPath},
			{"generated Go protobuf", at("api/v1/user.pb.go", 10), "", domain.SkipDeniedPath},
			{"generated Python protobuf", at("api/user_pb2.py", 10), "", domain.SkipDeniedPath},
			{"PHP protobuf metadata", at("generated/GPBMetadata/User.php", 10), "", domain.SkipDeniedPath},
			{"mocks", at("internal/mocks/store.go", 10), "", domain.SkipDeniedPath},
			{"protobuf source", at("api/v1/user.proto", 10), domain.RoleProd, ""},
			// Linguist's (^|/)cache/ and (^|/)env/ are left out of the
			// packaged list: those directories usually hold first-party code.
			{"a cache package", at("internal/cache/lru.go", 10), domain.RoleProd, ""},
			{"an env package", at("internal/env/env.go", 10), domain.RoleProd, ""},
			// So are build/ and storage/, beyond Laravel's storage/framework.
			{"a build package", at("src/go/build/build.go", 10), domain.RoleProd, ""},
			{"a storage package", at("internal/storage/postgres.go", 10), domain.RoleProd, ""},
			{"Laravel's compiled views", at("storage/framework/views/0a1b.php", 10), "", domain.SkipDeniedPath},

			// Only a regular file gets a role: a symbolic link's content is
			// its target, and a submodule has no lines. The deny list still
			// comes first.
			{"a symbolic link", notFile(at("link.py", 7)), "", domain.SkipUnclassified},
			{"a submodule", notFile(at("libs/parser", -1)), "", domain.SkipUnclassified},
			{"a denied link", notFile(at("vendor/link.py", 7)), "", domain.SkipDeniedPath},

			// too_big: strictly above [blame] max_bytes, and only with a
			// known size.
			{"at the limit", at("service.py", 400000), domain.RoleProd, ""},
			{"above the limit", at("service.py", 400001), "", domain.SkipTooBig},
			{"unknown size", at("service.py", -1), domain.RoleProd, ""},
			{"denied before too big", at("vendor/big.py", 1<<30), "", domain.SkipDeniedPath},
			// A path rule's max_bytes: data under a test directory larger
			// than 100 KB is test data, too big to count as test code
			// (httpx's tests/models/whatwg.json, 208,844 bytes); smaller
			// data, data of unknown size and code there stay test.
			{"large test data", at("tests/models/whatwg.json", 208844), "", domain.SkipTooBig},
			{"small test data", at("tests/models/cases.json", 5000), domain.RoleTest, ""},
			{"test data at the bound", at("tests/models/cases.json", 100000), domain.RoleTest, ""},
			{"test data over the bound", at("tests/models/cases.json", 100001), "", domain.SkipTooBig},
			{"test data of unknown size", at("tests/models/cases.json", -1), domain.RoleTest, ""},
			{"large test code", at("tests/test_options.py", 134294), domain.RoleTest, ""},
			// A directory rule matches directories only.
			{"a file named like a denied directory", at("scripts/coverage", 203), "", domain.SkipUnclassified},
			{"under a denied directory", at("coverage/lcov.info", 203), "", domain.SkipDeniedPath},

			// The header sniff, which reads Head only.
			{"generated header", withHeader(at("gen_out.py", 110)), "", domain.SkipGeneratedHeader},
			{"too big before the header", withHeader(at("gen_out.py", 400001)), "", domain.SkipTooBig},
			{"denied before the header", withHeader(at("vendor/gen.py", 10)), "", domain.SkipDeniedPath},
			{"no header sniff off Head", withHeader(gone(at("app.py", 188))), domain.RoleProd, ""},
			// Only a path a rule counts is generated: one no rule gives a
			// role stays unclassified, however widely the sniff read.
			{"a header on a path with no role", withHeader(at("notes.txt", 50)), "", domain.SkipUnclassified},
			{"a header on a path only a path rule gives a role", withHeader(at("requirements.txt", 50)), "", domain.SkipGeneratedHeader},
			{"the Fabric tasks", at("fabfile.py", 120), domain.RoleIaC, ""},
			{"a path off Head keeps its role", gone(at("old_name.py", 18)), domain.RoleProd, ""},
			{"a path off Head is denied too", gone(at("vendor/old.py", 18)), "", domain.SkipDeniedPath},
		} {
			got := classify.Classify(p, c.in)
			want := domain.PathClass{Path: c.in.Path, Role: c.role, Skip: c.skip, Size: c.in.Size, AtHead: c.in.AtHead}
			if got != want {
				t.Errorf("%s: Classify(%+v) = %+v, want %+v", c.name, c.in, got, want)
			}
		}
	})

	// The linguist attributes, read as GitHub Linguist reads them: unset and
	// the value false turn an attribute off, set and any other value turn it
	// on. Off un-denies, on denies, and on wins over off. An explicit
	// linguist-generated also settles the header sniff; linguist-vendored
	// says nothing about generated code.
	t.Run("attributes", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			path     string
			gen, ven string
			header   bool
			role     domain.Role
			skip     domain.Skip
		}{
			{"no attributes, denied", "vendor/lib.py", "unspecified", "unspecified", false, "", domain.SkipDeniedPath},
			{"no attributes, not denied", "service.py", "", "", false, domain.RoleProd, ""},
			{"deny plus -linguist-generated", "vendor/lib.py", "unset", "unspecified", false, domain.RoleProd, ""},
			{"deny plus -linguist-vendored", "vendor/lib.py", "unspecified", "unset", false, domain.RoleProd, ""},
			{"no deny plus linguist-generated", "api/client.py", "set", "unspecified", false, "", domain.SkipDeniedPath},
			{"no deny plus linguist-vendored", "lib/chart.js", "unspecified", "set", false, "", domain.SkipDeniedPath},
			{"deny, one unset, the other set", "vendor/lib.py", "unset", "set", false, "", domain.SkipDeniedPath},
			{"deny, one set, the other unset", "vendor/lib.py", "set", "unset", false, "", domain.SkipDeniedPath},
			{"linguist-vendored=false un-denies", "vendor/lib.py", "unspecified", "false", false, domain.RoleProd, ""},
			{"linguist-generated=true denies", "api/client.py", "true", "unspecified", false, "", domain.SkipDeniedPath},
			{"any other value denies", "api/client.py", "yes", "unspecified", false, "", domain.SkipDeniedPath},
			{"-linguist-generated settles the header", "api/client.py", "unset", "unspecified", true, domain.RoleProd, ""},
			{"linguist-generated=false settles the header", "api/client.py", "false", "unspecified", true, domain.RoleProd, ""},
			{"-linguist-vendored leaves the header", "api/client.py", "unspecified", "unset", true, "", domain.SkipGeneratedHeader},
		} {
			in := classify.Input{Path: c.path, AtHead: true, Size: 100, Generated: c.gen, Vendored: c.ven, HeaderHit: c.header}
			got := classify.Classify(p, in)
			want := domain.PathClass{Path: c.path, Role: c.role, Skip: c.skip, Size: 100, AtHead: true}
			if got != want {
				t.Errorf("%s: Classify(%+v) = %+v, want %+v", c.name, in, got, want)
			}
		}
	})

	// Explain names the rule that decided each class Classify gives, from
	// the policy alone: the step, the rule, where the policy holds it,
	// where it lives, and why when the policy says.
	t.Run("explain", func(t *testing.T) {
		in := func(path string, size int64) classify.Input {
			return classify.Input{Path: path, AtHead: true, Size: size, Generated: "unspecified", Vendored: "unspecified"}
		}
		for _, c := range []struct {
			name  string
			in    classify.Input
			kind  classify.RuleKind
			text  string
			where string
			src   string
			note  string // a substring of the note
		}{
			{"an extension", in("src/lib.rs", 10), classify.RuleExtension, ".rs", "[roles.extensions] prod", classify.SourcePackaged, ""},
			{"a name", in("deploy/Dockerfile", 10), classify.RuleName, "Dockerfile", "[roles.names] iac", classify.SourcePackaged, ""},
			{"a path rule", in("examples/hello/main.rs", 10), classify.RulePath, `(^|/)_?[Ee]xamples?/|^([Ss]amples?|[Dd]emos?)/`,
				"[[roles.path]] rule 4", classify.SourcePackaged, "Example code documents the product"},
			{"a deny pattern", in("web/app.min.js", 10), classify.RuleDeny, `(\.|-)min\.(js|css)$`, "generated_paths.txt line ",
				classify.SourcePackaged, "Minified JavaScript and CSS"},
			{"an attribute", classify.Input{Path: "api/client.py", AtHead: true, Size: 10, Generated: "set"}, classify.RuleAttribute,
				"linguist-generated or linguist-vendored", ".gitattributes", classify.SourceAttribute, "no deny pattern"},
			{"an attribute un-denies", classify.Input{Path: "vendor/keep.py", AtHead: true, Size: 10, Generated: "unset"},
				classify.RuleExtension, ".py", "[roles.extensions] prod", classify.SourcePackaged, ""},
			{"[blame] max_bytes", in("service.py", 400001), classify.RuleSize, "max_bytes = 400000", "[blame] max_bytes",
				classify.SourcePackaged, "never blamed"},
			// The rule that bounds test data decides small files too, and its
			// reason says what it does with both.
			{"a rule's max_bytes", in("tests/models/whatwg.json", 208844), classify.RuleSize, "max_bytes = 100000",
				"[[roles.path]] rule 1", classify.SourcePackaged, "test up to 100 KB"},
			{"small test data", in("tests/small.json", 9), classify.RulePath, "(^|/)(tests|test|__tests__|spec|testing)/",
				"[[roles.path]] rule 1", classify.SourcePackaged, "test up to 100 KB"},
			{"a generated header", classify.Input{Path: "gen_out.py", AtHead: true, Size: 10, HeaderHit: true}, classify.RuleHeader,
				"a generated-file header in the first 40 lines", "", classify.SourceHeader, ""},
			{"a link", classify.Input{Path: "link.py", AtHead: true, Size: 7, NotFile: true}, classify.RuleNotFile,
				"a symbolic link or a submodule", "", classify.SourceFileType, ""},
			{"no rule", in("notes.csv", 10), classify.RuleNone, "no name, extension or path rule", "", classify.SourcePackaged, "makes it count"},
			// A submodule has no size at HEAD, and no rule gives it a role:
			// a role for it would change nothing.
			{"a submodule", classify.Input{Path: "libs/sub", AtHead: true, Size: -1, NotFile: true}, classify.RuleNotFile,
				"no size at HEAD, as for a submodule", "", classify.SourceFileType, "a submodule gets no role"},
		} {
			got := classify.Explain(p, classify.Classify(p, c.in))
			if got.Kind != c.kind || got.Text != c.text || !strings.HasPrefix(got.Where, c.where) || got.Source != c.src ||
				!strings.Contains(got.Note, c.note) {
				t.Errorf("%s: Explain = %+v, want %s %q at %q from %s, noting %q", c.name, got, c.kind, c.text, c.where, c.src, c.note)
			}
		}
		// What a path adds, its size or an attribute's override, is its
		// Detail, never the rule's Note, which a legend prints once for
		// every path the rule decides.
		for _, c := range []struct {
			in           classify.Input
			note, detail string
		}{
			{in("tests/models/whatwg.json", 208844), "Configuration and data under a test directory are test up to 100 KB", "208844 bytes"},
			{in("tests/edge.json", 100001), "Configuration and data under a test directory are test up to 100 KB", "100001 bytes"},
			{classify.Input{Path: "vendor/keep.py", AtHead: true, Size: 10, Generated: "unset"}, "",
				"deny pattern (^|/)vendors?/ matches it, but .gitattributes turns linguist-generated or linguist-vendored off"},
		} {
			if got := classify.Explain(p, classify.Classify(p, c.in)); got.Note != c.note || got.Detail != c.detail {
				t.Errorf("Explain(%s) note %q, detail %q; want %q, %q", c.in.Path, got.Note, got.Detail, c.note, c.detail)
			}
		}
		// A class this classifier would not give, as an older stillhere
		// may have stored, has no rule it can name.
		if got := classify.Explain(p, domain.PathClass{Path: "src/lib.rs", Role: domain.RoleTest, Size: 10, AtHead: true}); got.Kind != classify.RuleUnknown {
			t.Errorf("a stored class the classifier would not give: Explain = %+v, want unknown", got)
		}
	})

	// A rule's pattern decides alone what a segment is: (^|/)test(/|$) does
	// not match "tests", and a multi-segment directory needs the whole
	// sequence in one pattern.
	t.Run("segments", func(t *testing.T) {
		own, err := policy.Parse("segments.toml", []byte(`
version = 1
[headline]
metric = "prod_nonblank_lines"
[week]
days = 7
sort = "lines_added"
[blame]
max_bytes = 400000
[bots]
fragments = []
[ai]
version = 1
coauthor_emails = []
agent_authors = []
[roles.extensions]
prod = [".py"]
[roles.names]
[[roles.path]]
role = "test"
pattern = '(^|/)test(/|$)'
[paths]
deny = ['(^|/)bootstrap/cache(/|$)']
`))
		if err != nil {
			t.Fatalf("policy.Parse: %v", err)
		}
		for _, c := range []struct {
			path string
			role domain.Role
			skip domain.Skip
		}{
			{"test/x.py", domain.RoleTest, ""},
			{"a/test/x.py", domain.RoleTest, ""},
			{"test", domain.RoleTest, ""}, // the segment may be the whole path; this rule has no when
			{"tests/x.py", domain.RoleProd, ""},
			{"latest/x.py", domain.RoleProd, ""},
			{"bootstrap/cache/config.py", "", domain.SkipDeniedPath},
			{"x/bootstrap/cache", "", domain.SkipDeniedPath},
			{"bootstrap/cached/config.py", domain.RoleProd, ""},
			{"cache/bootstrap/config.py", domain.RoleProd, ""},
		} {
			in := classify.Input{Path: c.path, AtHead: true, Size: 1}
			want := domain.PathClass{Path: c.path, Role: c.role, Skip: c.skip, Size: 1, AtHead: true}
			if got := classify.Classify(own, in); got != want {
				t.Errorf("Classify(%q) = %+v, want %+v", c.path, got, want)
			}
		}
	})
}
