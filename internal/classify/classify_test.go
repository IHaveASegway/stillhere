package classify_test

import (
	"testing"

	"github.com/IHaveASegway/stillhere/internal/classify"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// TestClassify pins the ordered classifier under the packaged policy
// (docs/spec/classification.md): roles and their precedence, segment
// matching, the deny list, too_big, the header sniff, and how .gitattributes
// overrides the deny list.
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
			{"tests/fixtures/x.py", domain.RoleTest},
			{"tests/migrations/x.py", domain.RoleTest},
			{"migrations/seeds/x.py", domain.RoleMigration},
		} {
			if got := classify.Role(p, c.path); got != c.want {
				t.Errorf("Role(%q) = %q, want %q", c.path, got, c.want)
			}
		}
	})

	t.Run("order", func(t *testing.T) {
		at := func(path string, size int64) classify.Input {
			return classify.Input{Path: path, AtHead: true, Size: size, Generated: "unspecified", Vendored: "unspecified"}
		}
		withHeader := func(in classify.Input) classify.Input { in.HeaderHit = true; return in }
		gone := func(in classify.Input) classify.Input { in.AtHead = false; return in }
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

			// too_big: strictly above [blame] max_bytes, and only with a
			// known size.
			{"at the limit", at("service.py", 400000), domain.RoleProd, ""},
			{"above the limit", at("service.py", 400001), "", domain.SkipTooBig},
			{"unknown size", at("service.py", -1), domain.RoleProd, ""},
			{"denied before too big", at("vendor/big.py", 1<<30), "", domain.SkipDeniedPath},

			// The header sniff, which reads Head only.
			{"generated header", withHeader(at("gen_out.py", 110)), "", domain.SkipGeneratedHeader},
			{"too big before the header", withHeader(at("gen_out.py", 400001)), "", domain.SkipTooBig},
			{"denied before the header", withHeader(at("vendor/gen.py", 10)), "", domain.SkipDeniedPath},
			{"no header sniff off Head", withHeader(gone(at("app.py", 188))), domain.RoleProd, ""},
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
