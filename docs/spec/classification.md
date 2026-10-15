# Classification

Status: carried over from the 2026-10-07 plan, updated 2026-10-14 (RE2 patterns, `check-attr --source`; then, with #5's code, Linguist's attribute values, the curated deny list, the exact header sniff, `Test.php`; then, from #5's review, a header sniff that reads no attribute and takes descriptions only where a line starts, `storage` narrowed to Laravel's and `build` left out, test directories for code only, links and submodules; then, with #57, roles for 33 more extensions and 2 names: the issue's Haskell, Elixir with its templates, Tcl, zsh and Kotlin script; more common extensions of languages the list had (Shell, TypeScript, C++, Python, HCL); ERB and Gradle's Groovy scripts; Astro's components, `prod` by the rule that already made `.vue` and `.svelte` so (a component file holds code), though Linguist types all three markup; and the programming languages among GitHub's 40 most pushed that had no role, except Dart; then, from #57's review, which extensions the list leaves out, [#65](https://github.com/IHaveASegway/stillhere/issues/65)) · the week predicate becomes tests in M0, blame roles in M1

There is one ordered classifier. The week collector and the blame collector both call it; there is no second, looser filter.

## Order

1. **Denied path**, decided by the [deny list](#deny-list) and then [.gitattributes](#gitattributes): adds 0 to `lines_added`, is not blamed, and counts toward `skipped.denied_path`.
2. **Too big**, size greater than `[blame].max_bytes` (400000 in the packaged policy): same, counts toward `skipped.too_big`. Sizes come from `git ls-tree -r -l -z <sha>` for paths present at the scanned SHA. A path the history touched (week or scan) that is absent at that SHA is sized with one batched `git cat-file --batch-check='%(objecttype) %(objectsize)' -Z` over NUL-terminated `<commit>:<path>` lines (`-Z`, Git 2.42+, makes the output NUL-terminated too, which a path holding a newline needs), where the commit is the newest that added lines to the path; a path missing from that tree has an unknown size (`-1`) and skips this check. In a blobless partial clone `ls-tree -l` prints `BAD` for a missing blob's size (verified on 2.50.1), which is also unknown. No blame is spawned just to learn a size.
3. **Generated header**, from the [header sniff](#header-sniff): same, counts toward `skipped.generated_header`, unless `.gitattributes` turns `linguist-generated` off for the path ([.gitattributes](#gitattributes)).
4. **Role assigned**: the path survives. Any role counts toward week `lines_added`; only `prod` counts toward the still-here headline. Only a regular file gets a role.
5. **No role**: adds 0, is not blamed, and counts toward `skipped.unclassified`. That includes a symbolic link (mode `120000`), whose content is its target's name, and a submodule (a gitlink), which has no lines, although numstat counts a link's target and a submodule bump as a changed line (verified on 2.50.1). A path the scanned SHA no longer has is classified as a file: `cat-file --batch-check` has no `%(objectmode)` (verified on 2.50.1), so it cannot tell a deleted link from a file.

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
- **Two segments of stillhere's own are left out for the same reason**, as the file's last section records; Linguist has neither. Across the 175,079 files with a role in 1,120 Go modules (a module cache, 2026-10-14), the files under either segment were the modules' own packages, scripts and test data, never vendored code or build output:
  - `(^|/)build(/|$)`: 221 files, such as Go's `go/build` package and Thrift's `build/docker` scripts. Build output is in every common `.gitignore` template, and a repository that commits it marks it `linguist-generated`.
  - `(^|/)storage(/|$)`: 1,083 files, such as go-git's `storage/` and Kubernetes' `storage/v1` API types. The rule was for Laravel, so the list denies Laravel's `storage/framework` (compiled Blade views, caches, sessions) and `storage/logs` instead.
- **User file.** `[paths] deny = [...]` is the whole list. Setting `package_file`, setting both keys, or setting neither exits 2, because a user file's hash would not cover a packaged text file. A user who wants the packaged patterns copies them into `deny`.
- **Minimum packaged contents:**
  - single segments, each written as `(^|/)<segment>(/|$)`: `node_modules`, `vendor`, `dist`, `coverage`, `\.next`, `out`, `third_party`, `__pycache__`, `\.venv`, `venv`;
  - Laravel's generated directories, as consecutive segments: `(^|/)bootstrap/cache(/|$)` and `(^|/)storage/(framework|logs)(/|$)`;
  - lock and sum files, including `go.sum`;
  - minified assets (`\.min\.js$`);
  - generated suffixes (`\.pb\.go$`, `_pb2\.py$`);
  - mock and `gen/` directories;
  - generated PHP under protobuf output (`gpbmetadata`).

  Lockfiles, vendor directories and generated suffixes live only here, never in a second hardcoded set. Protobuf `.proto` sources stay `prod`.

## .gitattributes

After the deny patterns, one batched call per repo, so git does the reading. Attribute macros and `info/attributes` are never reimplemented.

```
git <common> check-attr -z --source=<sha> --stdin linguist-generated linguist-vendored
```

Stdin is NUL-terminated paths: every path at the scanned SHA, plus each touched path that SHA no longer has, which is matched against the attributes at the scanned SHA too. The output is `path\0attribute\0info\0`, where info is `unspecified`, `unset`, `set`, or a value. An attribute is **on** when info is `set` or any value other than `false`, and **off** when info is `unset` or the value `false`. That is how GitHub Linguist reads a boolean attribute (`boolean_attribute` in `lib/linguist/lazy_blob.rb`), so `linguist-vendored=false`, a common way to un-vendor a path, means what it means on GitHub. An empty value (`linguist-generated=`) is on too: check-attr prints it as an empty info (verified on 2.50.1), while libgit2, which Linguist reads attributes with, leaves an assignment with an empty value set. Per path, in this order:

1. `denied` starts true if any deny pattern matched.
2. If either attribute is off, `denied` becomes false. An explicit `-linguist-generated` or `-linguist-vendored` (or `=false`) is the only un-deny.
3. If either attribute is on, `denied` becomes true. On wins over off on the other attribute.

`linguist-generated` turned off also skips the [header sniff](#header-sniff) step: in Linguist an explicit `linguist-generated` decides whether a file is generated, ahead of every content heuristic, and without it a team could never count a hand-maintained file whose header says `DO NOT EDIT`, because the marker list is source, not policy. `linguist-vendored` says nothing about generated code, so turning it off leaves the sniff in place.

`linguist-documentation` is not read. Linguist leaves documentation out of a repository's language bar, but stillhere does not drop documentation: a Markdown file has the `doc` role, which week `lines_added` counts and the still-here headline does not. A team whose example code should not count as `prod` gives it the `doc` role with a policy path rule. Reading the attribute as the `doc` role is a possible extension, for the owner to decide.

`--source=<sha>` (Git 2.40+) is required. Without it, a bare mirror reports `unspecified` for every attribute (verified), and a work-tree clone reads the checked-out files instead of the scanned SHA, which makes the result depend on whatever branch the user has checked out. `<common>` pins `core.ignoreCase=false`, which makes patterns match the same way on every machine (until #13 it was set on this command alone): a clone on macOS or Windows sets `core.ignorecase=true`, under which `*.PY linguist-generated` marks `a.py` (verified on 2.50.1), while a Linux clone, like GitHub's servers, matches case-sensitively. A repository's `attr.tree` does not change the answer, because `--source` takes precedence over it: verified on 2.50.1, and in git 2.45.0's source `check-attr --source` sets the same source as `git --attr-source`, which `attr.tree` fills only when nothing set it. `TestAttributes` sets `attr.tree`, so CI checks this on both git versions it runs. The runner points `core.attributesFile` at `/dev/null` and sets `GIT_ATTR_NOSYSTEM=1`, which drops only the per-user and system files; the repository's `.gitattributes` and `info/attributes` still apply ([git-invocation.md](git-invocation.md#analysis-commands-offline)). In a blobless partial clone the `.gitattributes` blob is absent and lazy fetch is off, so every attribute reads `unspecified` (verified); a partial clone has no line facts, so nothing depends on it.

## Header sniff

Once per repo, at the scanned SHA. This is cheaper than opening every file:

```
git <common> grep --no-color --no-column -n -o -z -a -i -E -m 1 -e '<gate>' \
  --and \( -e '<described>' -e '<do not modify>' -e '<declared>' \) <sha> -- \
  ':(icase)*<ext>' ... '<name>' '*/<name>' ...
```

A header marks a file generated in one of two ways. Both are matched case-insensitively, and each marker ends at a word boundary, so the identifier `AutogeneratedPos` is none:

- **A declaration**: `do not edit`, `@generated`, or `<auto-generated` (also `<autogenerated`, .NET's convention). It counts anywhere in a line's first 100 characters, unless a quote (`"`, `'`, a backtick) comes before it there, because then it sits in a string literal: a generator program that prints its output's header, such as `fmt.Fprintln(&b, "// Code generated by mkbuiltin.go. DO NOT EDIT.")`, is no generated file. `do not modify` is a declaration only as a line of its own (`// DO NOT MODIFY!`, `Do not modify this file by hand.`); in a sentence it is advice, as in `Do not modify a Record after handing out a copy to it` (Go's `log/slog`).
- **A description**: `code generated`, `generated by`, `auto-generated`, `autogenerated`, `auto generated`, `automatically generated` or `machine generated`, or a subject that says so: "this file (or code, class, header, module, program, source, document) is (or was, has been) generated", with optional words such as "entire", "(mostly)", "auto-" or "automatically", and "this is a generated". It counts only as the first words of a line's text: after at most 32 characters of indentation and comment punctuation (`//`, `#`, `/*`, `*`, `<!--`, `--`, `;`), none of them a quote, and optionally after a warning word such as `WARNING:`, `NOTE:` or `DO NOT MODIFY.`. A docstring line starts there too. In mid-sentence the same words describe something else: `Random numbers are generated by a [Source]` in Go's `math/rand`.
- **The gate** is `generated`, `do not edit` or `do not modify`, which every match of a rule contains. git tests the rules only on lines that match the gate (`--and`), so the sniff is faster than the plain marker list it replaced: 0.8 s on Go's source tree (8,393 files with a role, git 2.50.1 on macOS), against 1.8 s for the old list and 3.2 s for the rules without the gate. Gated and ungated runs gave the same paths and lines on Go's tree and the macOS SDK headers.

The rules were chosen on the files with a role that the packaged deny list keeps, in Go's source tree, Python's standard library, the macOS SDK headers and 1,120 Go modules (a module cache, 2026-10-14). git's ERE engine and Go's RE2 agreed on every file checked.

- No design missed any of the 21,220 files that follow Go's convention, `^// Code generated .* DO NOT EDIT\.$`.
- On Go's tree, the old list matched 67 other files, 60 of them hand-written (prose, identifiers, generator programs); these rules match 12, 7 of them generated (cgo's `defs_*.go`, an `instgen` table).
- The alternative, every marker at the start of a comment, misses a header that opens with a warning or a subject: 4,842 more files in the module cache, such as the 3,338 files of Pulumi's AWS SDK (`*** WARNING: this file was generated by`), 1,118 of the Azure SDK for Go (`This code was auto-generated by:`) and the 68 generated `.proto` files of Kubernetes, and 76 macOS SDK headers (Perl's `DO NOT EDIT THIS FILE`, autoconf's `This file is automatically generated by configure`).

- The pathspecs are the policy's role extensions, matched at any depth and ignoring case as the classifier does, and its exact role names at any depth, glob characters escaped; a policy with neither has no pathspec, so every file is read. A file whose only role comes from a path rule without `when` is not sniffed. The packaged policy has no such rule for a file whose name gives it no role, because its test-segment rule admits code and configuration only ([Roles](#roles)).
- `-a` reads every file as text, so no attribute changes the sniff. `-I` would decide what is binary from the `diff` attribute, which git grep reads from the work tree, not from the scanned SHA, while a bare mirror reads none: a generated file marked `-diff` or `binary` was sniffed in a bare mirror but not in a work tree of the same SHA, and an uncommitted `.gitattributes` edit flipped the result (verified on 2.50.1). `git --attr-source=<sha>` (Git 2.41+) would make `-I` read the scanned SHA, but then a file marked `-diff`, often a generated one, would never be sniffed. The pathspecs keep binary files out in practice.
- `-m 1` (Git 2.38+) stops at a file's first matching line, the only one that can decide the line-40 rule, and `-o` prints only what matched, which the rules bound, so a minified file's megabyte-long line never reaches stillhere.
- Output records are `<sha>:<path>\0<line>\0<match>\n`, one for each match on the line, the gate's included (verified, also in bare mirrors); a path may hold a newline, so a record ends at the first newline after its second NUL. Strip the `<sha>:` prefix before using the path.
- `--no-column` is a call-site rule like `--no-color`: a repo-local `grep.column=true` adds a column field to the output (verified on 2.50.1).
- A path counts only when its matched line number is 40 or less.
- Exit status 1 means no hits, not a failed repo.
- The markers are source, not policy: the patterns are constants in `internal/collect`, and the blame cache keys on them.
- A path absent at the scanned SHA gets no header sniff, because the sniff reads that SHA. The deny list, attributes, size and roles still apply to it.

## Roles

The default role map. The still-here headline counts `prod` only.

| Role | Extensions, names, patterns |
| --- | --- |
| prod | `.php .go .ts .tsx .js .jsx .mjs .cjs .py .rb .java .kt .swift .m .mm .c .h .cc .cpp .hpp .rs .cs .vue .svelte .proto .graphql .gql .lua`; more common extensions of those languages, `.mts .cts .cxx .hxx .hh .pyi`; Astro's components, `.astro`; Haskell `.hs`, Elixir `.ex .exs`, Tcl `.tcl`, Perl `.pl .pm`, R `.r`, assembly `.asm .s` |
| sql | `.sql` |
| markup | `.css .scss .sass .less .html .htm .storyboard .xib`; templates: ERB `.erb`, EEx `.eex .heex .leex` |
| iac | `.yml .yaml .tf .tfvars .hcl .toml .xml .json`; shell scripts `.sh .bash .zsh .zsh-theme`, Windows `.bat .cmd` and PowerShell `.ps1 .psm1 .psd1`; builds: Gradle's `.kts .gradle`, CMake's `.cmake`, Nix's `.nix`; plus the names `Dockerfile`, `Makefile`, `Containerfile`, `CMakeLists.txt`, `Procfile` |
| doc | `.md .rst .adoc` |
| test | segments `tests`, `test`, `__tests__`, `spec`, `testing`, when the extension or name role would have been `prod`, `sql`, `markup` or `iac`; names ending `_test.go`, `.test.ts`, `.test.tsx`, `.spec.ts`, `.spec.tsx`, `.test.js`, `.spec.js`, `Test.php`, `Tests.swift` (case-sensitive: PHPUnit's `UserTest.php` and XCTest's `AppTests.swift`; a lowercase `test.php` would also take `latest.php`) |
| migration | segment `migrations`, when the extension role would have been `prod` |
| seed | segments `seeders`, `factories`, `seeds`, `fixtures`, when the extension role would have been `prod` or `sql` |

An extension or name gets the role of what its files are for, which is not always GitHub Linguist's type for its language: source code is `prod` (a component file too, as `.vue`, `.svelte` and `.astro` are, though Linguist types them markup); shell scripts and build and deployment definitions are `iac`, as `Makefile` and `Dockerfile` are, though Linguist types Shell, Batchfile, PowerShell, CMake and Nix programming; views and templates are `markup`, as HTML is. So one language's extensions can take different roles: Kotlin's `.kt` is `prod` and its `.kts` `iac` (below), and of the extensions Linguist gives XML, `.xml` is `iac` while Interface Builder's `.storyboard` and `.xib` are `markup`.

The list holds each language's common extensions, not every one Linguist knows (checked against `languages.yml` at `73ee9a3b277f`, the commit the deny list was curated from). An extension that another language also claims stays out, as `.inc`, `.i`, `.t`, `.cgi`, `.fcgi`, `.spec`, `.cp`, `.re` and `.workflow` do, because one role would be wrong for some of its files. So do less common ones, and their files get no role: among the languages listed, C++'s `.ino`, `.ipp`, `.inl`, `.tpp`, `.cppm` and `.ixx`, Haskell's `.hsc` and `.hs-boot`, Tcl's `.tm`, the Korn shell's `.ksh` and OpenTofu's `.tofu`, with a longer tail for JavaScript, Ruby and PHP ([#65](https://github.com/IHaveASegway/stillhere/issues/65), which weighs mapping roles from Linguist's data instead of a hand list).

Two cases decided by what their files are for rather than by their language, added by #57:

- **Kotlin script (`.kts`) is `iac`.** Its files are Gradle builds: of the 430 `.kts` files in seven Kotlin repositories (`square/okhttp`, a corpus repository of #16, and kotlinx.coroutines, nowinandroid, ktor, koin, detekt and leakcanary, at HEAD on 2026-10-14), 426 are `*.gradle.kts`, build scripts and convention plugins; the other four are a Spotless license header, a test resource and two `*.main.kts` automation scripts. Linguist names `.gradle.kts` a language of its own, Gradle Kotlin DSL, of type data. Gradle's Groovy scripts (`.gradle`, Linguist's Gradle, also data) had no role, so they take the same one: a build whose scripts move from Groovy to Kotlin keeps its classification.
- **EEx and ERB templates are `markup`.** Linguist types HTML+EEX (`.html.eex`, `.heex`, `.leex`) and HTML+ERB (`.erb`) markup, and a server-side view is what `.html` already is for Django, Jinja and Angular templates. ERB had no role before #57 either, so Rails views counted nowhere. A template that generates code, such as Phoenix's generators (`priv/templates/*.ex.eex`), is `markup` too.

Path roles override extension roles. The 2026-10-07 plan left the precedence among path roles implicit (for example `tests/fixtures/x.py`). The precedence is test, then migration, then seed, pinned by `TestClassify`. Path rules are tried in that order, and the first rule whose pattern matches **and** whose condition admits the extension or name role wins: `db/migrations/seeds/x.sql` (extension role `sql`) is `seed`, because the migration rule admits only `prod`. An exact name wins over the extension, and the extension is the file name's last dot suffix, lowercased, so a policy may list only extensions with one dot (`.gz`, never `.tar.gz`, which a path rule can match). A rule without a condition admits any path. The test-segment rule has one: a file under a test directory is `test` when it is code or configuration, while data there gets no role (`tests/data.csv`, golden files, Jest's `__snapshots__/*.snap`, which Jest writes itself) and documentation stays `doc` (`tests/README.md`, and `docs/spec/`, a specification rather than an RSpec suite). Without it, every such file would count toward week `lines_added` as test code, and the header sniff would never read it. (Under the packaged deny list `tests/fixtures/` is denied anyway, by Linguist's test-fixture rule, so the precedence matters for what `classify.Role` reports.)

## Open decision #2: weights and gearing

Status: candidate design, built only if approved ([../product.md](../product.md#open-decisions)). Neither ever enters a headline, a sort key, or a rank.

- **Role weights:** prod 1.0, sql 0.7, markup 0.4 (`.storyboard` and `.xib` 0.2, as UI-definition XML), iac 0.35, doc 0.0, test 0.35, migration 0.5, seed 0.25. No per-repo tiers: no "ops repo" IaC at 1.0, and no hand-listed toy or legacy repos.
- **`geared_fp`** per person = Σ `nonblank × weight / sloc_per_fp` over non-denied files of every role. `geared_fp_estimated` is true when any term used an estimated row; it is false for a person with zero lines.
- **`sloc_per_fp`, measured:** the QSM Function Point Languages Table 5.0 **median of new plus modified logical SLOC per function point**, not physical lines and not lines per author, with `estimate = false`: C 99, C++ 53, C# 59, Java 53, JavaScript 53, HTML 40, SQL 21.
- **`sloc_per_fp`, estimated** (absent from that table, `estimate = true`): PHP 47, TypeScript 50, Go 48, Python 34, Swift 45, Objective-C 55, Kotlin 45, Ruby 25, Rust 45, CSS 55, proto 40, GraphQL 30, Vue 50, Svelte 50, Lua 35, shell 40, YAML 40, HCL 35, JSON 40, XML 50, Markdown 40. Unknown languages use 50.
- **Evidence from the proof run:** gearing did not change the top 10.
