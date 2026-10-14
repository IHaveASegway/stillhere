# Classification

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (RE2 patterns, `check-attr --source`; then, with #5's code, Linguist's attribute values, the curated deny list, the exact header sniff, `Test.php`) · the week predicate becomes tests in M0, blame roles in M1

There is one ordered classifier. The week collector and the blame collector both call it; there is no second, looser filter.

## Order

1. **Denied path**, decided by the [deny list](#deny-list) and then [.gitattributes](#gitattributes): adds 0 to `lines_added`, is not blamed, and counts toward `skipped.denied_path`.
2. **Too big**, size greater than `[blame].max_bytes` (400000 in the packaged policy): same, counts toward `skipped.too_big`. Sizes come from `git ls-tree -r -l -z <sha>` for paths present at the scanned SHA. A path the history touched (week or scan) that is absent at that SHA is sized with one batched `git cat-file --batch-check='%(objecttype) %(objectsize)' -Z` over NUL-terminated `<commit>:<path>` lines (`-Z`, Git 2.42+, makes the output NUL-terminated too, which a path holding a newline needs), where the commit is the newest that added lines to the path; a path missing from that tree has an unknown size (`-1`) and skips this check. In a blobless partial clone `ls-tree -l` prints `BAD` for a missing blob's size (verified on 2.50.1), which is also unknown. No blame is spawned just to learn a size.
3. **Generated header**, from the [header sniff](#header-sniff): same, counts toward `skipped.generated_header`, unless `.gitattributes` turns `linguist-generated` off for the path ([.gitattributes](#gitattributes)).
4. **Role assigned**: the path survives. Any role counts toward week `lines_added`; only `prod` counts toward the still-here headline.
5. **No role**: adds 0, is not blamed, and counts toward `skipped.unclassified`.

## Patterns

Every path pattern is a Go `regexp` (RE2), applied with `MatchString` to the full repo-relative path with forward slashes. RE2 has no lookaround and no backreferences, so Linguist's Ruby patterns are translated, not pasted. A pattern that `regexp.Compile` rejects fails the policy loader test.

Match path **segments**, not substrings. `(^|/)tests(/|$)` matches `tests/test_service.py`. A naive check that prepends a slash and searches for `/tests/` misses that path, because the characters `/tests/` never occur in it. `(^|/)test(/|$)` does not match `tests`. A multi-segment directory needs the whole sequence in one pattern, `(^|/)bootstrap/cache(/|$)`: comparing one path component to the string `bootstrap/cache` never matches `bootstrap/cache/config.php`.

## Deny list

Exactly one policy file governs a run: `--policy FILE`, else `./stillhere.toml`, else the packaged default. That file replaces the packaged file as a whole. There is no union and no deep merge. A missing required table, or a missing key in one, is a usage error (exit 2), never filled in from the package; the values these specs call defaults are the packaged file's.

- **Packaged default.** `[paths] package_file = "generated_paths.txt"`, with no `deny` key. That embedded text file is the whole deny list. Its header says each non-comment line is an RE2 pattern translated from GitHub Linguist's `vendor.yml` and generated-file rules (MIT, attributed), and records the upstream commit the curator actually read; no SHA is invented. The policy hash covers the TOML bytes, a length prefix, and this file, so editing a pattern changes the hash ([codebase.md](codebase.md#package-apis) gives the exact bytes).
- **What the packaged list holds.** The curator read Linguist at `73ee9a3b277f398c37ca4ab0dea0193849de6460` (main on 2026-10-14):
  - every `vendor.yml` pattern, copied unchanged: Linguist's own CI checks each one against Go's engine, and its allowlist of exceptions is empty;
  - the `generated.rb` rules that read only the path, with Ruby's delimiters removed and `/i` written `(?i)`; `go_vendor?` uses lookaround and is rewritten without it, as an equivalent DNS-label pattern;
  - none of the `generated.rb` rules that read file content, which the [header sniff](#header-sniff) covers in part;
  - two `vendor.yml` rules left out on purpose, commented in place with the reason. `(^|/)cache/` and `(^|/)env/` match directories that usually hold a team's own code, such as a Go `internal/cache` package. Linguist only drops such files from a language bar; stillhere would silently drop their authors' lines from every ownership and durability number. Laravel's generated `bootstrap/cache` and virtual environments (`venv`, `.venv`) are denied by the minimum contents below. A repository that keeps third-party code there marks it with `linguist-vendored`.
- **User file.** `[paths] deny = [...]` is the whole list. Setting `package_file`, setting both keys, or setting neither exits 2, because a user file's hash would not cover a packaged text file. A user who wants the packaged patterns copies them into `deny`.
- **Minimum packaged contents:**
  - single segments, each written as `(^|/)<segment>(/|$)`: `node_modules`, `vendor`, `dist`, `build`, `coverage`, `\.next`, `out`, `third_party`, `__pycache__`, `\.venv`, `venv`, `storage`;
  - `(^|/)bootstrap/cache(/|$)`;
  - lock and sum files, including `go.sum`;
  - minified assets (`\.min\.js$`);
  - generated suffixes (`\.pb\.go$`, `_pb2\.py$`);
  - mock and `gen/` directories;
  - generated PHP under protobuf output (`gpbmetadata`).

  Lockfiles, vendor directories and generated suffixes live only here, never in a second hardcoded set. Protobuf `.proto` sources stay `prod`.

## .gitattributes

After the deny patterns, one batched call per repo, so git does the reading. Attribute macros and `info/attributes` are never reimplemented.

```
git <common> -c core.ignoreCase=false check-attr -z --source=<sha> --stdin linguist-generated linguist-vendored
```

Stdin is NUL-terminated paths: every path at the scanned SHA, plus each touched path that SHA no longer has, which is matched against the attributes at the scanned SHA too. The output is `path\0attribute\0info\0`, where info is `unspecified`, `unset`, `set`, or a value. An attribute is **on** when info is `set` or any value other than `false`, and **off** when info is `unset` or the value `false`. That is how GitHub Linguist reads a boolean attribute (`boolean_attribute` in `lib/linguist/lazy_blob.rb`), so `linguist-vendored=false`, a common way to un-vendor a path, means what it means on GitHub. Per path, in this order:

1. `denied` starts true if any deny pattern matched.
2. If either attribute is off, `denied` becomes false. An explicit `-linguist-generated` or `-linguist-vendored` (or `=false`) is the only un-deny.
3. If either attribute is on, `denied` becomes true. On wins over off on the other attribute.

`linguist-generated` turned off also skips the [header sniff](#header-sniff) step: in Linguist an explicit `linguist-generated` decides whether a file is generated, ahead of every content heuristic, and without it a team could never count a hand-maintained file whose header says `DO NOT EDIT`, because the marker list is source, not policy. `linguist-vendored` says nothing about generated code, so turning it off leaves the sniff in place.

`--source=<sha>` (Git 2.40+) is required. Without it, a bare mirror reports `unspecified` for every attribute (verified), and a work-tree clone reads the checked-out files instead of the scanned SHA, which makes the result depend on whatever branch the user has checked out. `core.ignoreCase=false` makes patterns match the same way on every machine: a clone on macOS or Windows sets `core.ignorecase=true`, under which `*.PY linguist-generated` marks `a.py` (verified on 2.50.1), while a Linux clone, like GitHub's servers, matches case-sensitively. The runner points `core.attributesFile` at `/dev/null` and sets `GIT_ATTR_NOSYSTEM=1`, which drops only the per-user and system files; the repository's `.gitattributes` and `info/attributes` still apply ([git-invocation.md](git-invocation.md#analysis-commands-offline)). In a blobless partial clone the `.gitattributes` blob is absent and lazy fetch is off, so every attribute reads `unspecified` (verified); a partial clone has no line facts, so nothing depends on it.

## Header sniff

Once per repo, at the scanned SHA. This is cheaper than opening every file:

```
git <common> grep --no-color --no-column -n -o -z -I -i -E -m 1 -e '<markers joined by |>' <sha> -- \
  ':(icase)*<ext>' ... '<name>' '*/<name>' ...
```

The markers, matched case-insensitively, are `code generated`, `@generated`, `do not edit`, `auto-generated`, `autogenerated`, `automatically generated`, `generated by`, `this file is generated`, and `this file was generated`.

- The pathspecs are the policy's role extensions, matched at any depth and ignoring case as the classifier does, and its exact role names at any depth, glob characters escaped; a policy with neither has no pathspec, so every file is read. A path whose only role comes from a path rule, such as `tests/data.csv`, is not sniffed.
- `-m 1` (Git 2.38+) stops at a file's first matching line, the only one that can decide the line-40 rule, and `-o` prints only the matched marker, so a minified file's megabyte-long line never reaches stillhere.
- Output records are `<sha>:<path>\0<line>\0<marker>\n` (verified, also in bare mirrors); a path may hold a newline, so a record ends at the first newline after its second NUL. Strip the `<sha>:` prefix before using the path.
- `--no-column` is a call-site rule like `--no-color`: a repo-local `grep.column=true` adds a column field to the output (verified on 2.50.1).
- A path counts only when its matched line number is 40 or less.
- Exit status 1 means no hits, not a failed repo.
- The marker list is source, not policy.
- A path absent at the scanned SHA gets no header sniff, because the sniff reads that SHA. The deny list, attributes, size and roles still apply to it.

## Roles

The default role map. The still-here headline counts `prod` only.

| Role | Extensions, names, patterns |
| --- | --- |
| prod | `.php .go .ts .tsx .js .jsx .mjs .cjs .py .rb .java .kt .swift .m .mm .c .h .cc .cpp .hpp .rs .cs .vue .svelte .proto .graphql .gql .lua` |
| sql | `.sql` |
| markup | `.css .scss .sass .less .html .htm .storyboard .xib` |
| iac | `.yml .yaml .tf .hcl .toml .xml .json`, plus `Dockerfile`, `Makefile`, `Containerfile`, `*.sh` |
| doc | `.md .rst .adoc` |
| test | segments `tests`, `test`, `__tests__`, `spec`, `testing`; names ending `_test.go`, `.test.ts`, `.test.tsx`, `.spec.ts`, `.spec.tsx`, `.test.js`, `.spec.js`, `Test.php`, `Tests.swift` (case-sensitive: PHPUnit's `UserTest.php` and XCTest's `AppTests.swift`; a lowercase `test.php` would also take `latest.php`) |
| migration | segment `migrations`, when the extension role would have been `prod` |
| seed | segments `seeders`, `factories`, `seeds`, `fixtures`, when the extension role would have been `prod` or `sql` |

Path roles override extension roles. The 2026-10-07 plan left the precedence among path roles implicit (for example `tests/fixtures/x.py`). The precedence is test, then migration, then seed, pinned by `TestClassify`. Path rules are tried in that order, and the first rule whose pattern matches **and** whose condition admits the extension or name role wins: `db/migrations/seeds/x.sql` (extension role `sql`) is `seed`, because the migration rule admits only `prod`. An exact name wins over the extension, and the extension is the file name's last dot suffix, lowercased. A rule without a condition admits any path, so a file under `tests/` is `test` whatever its extension. (Under the packaged deny list `tests/fixtures/` is denied anyway, by Linguist's test-fixture rule, so the precedence matters for what `classify.Role` reports.)

## Open decision #2: weights and gearing

Status: candidate design, built only if approved ([../product.md](../product.md#open-decisions)). Neither ever enters a headline, a sort key, or a rank.

- **Role weights:** prod 1.0, sql 0.7, markup 0.4 (`.storyboard` and `.xib` 0.2, as UI-definition XML), iac 0.35, doc 0.0, test 0.35, migration 0.5, seed 0.25. No per-repo tiers: no "ops repo" IaC at 1.0, and no hand-listed toy or legacy repos.
- **`geared_fp`** per person = Σ `nonblank × weight / sloc_per_fp` over non-denied files of every role. `geared_fp_estimated` is true when any term used an estimated row; it is false for a person with zero lines.
- **`sloc_per_fp`, measured:** the QSM Function Point Languages Table 5.0 **median of new plus modified logical SLOC per function point**, not physical lines and not lines per author, with `estimate = false`: C 99, C++ 53, C# 59, Java 53, JavaScript 53, HTML 40, SQL 21.
- **`sloc_per_fp`, estimated** (absent from that table, `estimate = true`): PHP 47, TypeScript 50, Go 48, Python 34, Swift 45, Objective-C 55, Kotlin 45, Ruby 25, Rust 45, CSS 55, proto 40, GraphQL 30, Vue 50, Svelte 50, Lua 35, shell 40, YAML 40, HCL 35, JSON 40, XML 50, Markdown 40. Unknown languages use 50.
- **Evidence from the proof run:** gearing did not change the top 10.
