# Identity candidates

Status: M2 (#26) · 2026-10-14 · `identity.Candidates`, `app.Identities`, `stillhere identities` and the MCP server's `identity_candidates` · oracles in [SPEC.md](../../testdata/fixture/SPEC.md#identity-candidates-m2-26)

stillhere suggests `.mailmap` lines for addresses that may belong to one person, each with the evidence for it, and applies none. git's mailmap stays the only identity store ([ADR 0004](../adr/0004-github-behind-sync-boundary.md), [git-invocation.md](git-invocation.md#identity-mailmap-and-bots)): a candidate becomes a merge only when a person writes its line into a reviewed file.

## Why

Identity is the weakest joint of an authorship report. One person with several addresses splits into several people, and every metric that groups by person splits with them: without the fixture's mailmap, Ada's 10 surviving lines become 7 and `knowledge_loss@1` goes from 4/34 to 11/34 ([SPEC.md](../../testdata/fixture/SPEC.md#knowledge_loss1-main-fixture)). A wrong merge is worse: it invents a person who does not exist, with two people's work. The report's `unmerged-identities` caveat says where a line may be missing, but not which line; this command writes the line and the evidence a person needs to decide, so the fix lands where the data lives.

## Data ownership

| Data | System of record | Writes it | Reads it | How it crosses |
| --- | --- | --- | --- | --- |
| Email aliases | `.mailmap`: each repository's own, and the organisation's file `--mailmap` names | People, in reviewed commits | git, at every analysis command | git applies them while a scan runs (`-c mailmap.file=`) |
| Candidates | None: derived when asked from one stored run, never stored | `identity.Candidates` | `stillhere identities`, the MCP server | In memory, as a `report.Identities` |
| GitHub logins | GitHub | GitHub | `sync github` | The forge replica (`ForgeCommit`) |

No second owner: stillhere writes no mailmap and keeps no people table ([architecture.md](../architecture.md)). The candidates read one run, so one HEAD set and one mailmap; a line added since takes effect at the next scan, which every answer says in its `mailmap-at-scan` caveat.

## Prior art

- **`git shortlog -sne` and a hand-curated `.mailmap`** is the baseline, and the store this command feeds. The Linux kernel keeps a reviewed `.mailmap` in its tree, and gitdm, which LWN uses to count kernel contributions, keeps an alias file of its own.
- **GrimoireLab SortingHat** (CHAOSS) is a full identity database with merge tooling. Its `recommend_matches` matches email, name or username by exact equality, merges overlapping match sets, so A–B and B–C become one group, and leaves out terms in an exclusion table. It is not reused: a Python service with its own database would be a second owner of identity, and its overlapping-set merge chains, which this spec allows only at low confidence.
- **Research.** Bird et al., "Mining email social networks" (MSR 2006), gave the name-and-address heuristics later work builds on; Goeminne and Mens, "A comparison of identity merge algorithms for software repositories" (Science of Computer Programming 78(8), 2013), compare such algorithms. Amreen et al., "ALFAA" (arXiv 1901.03363), found commits from different environments, misspellings, organisational ids, default values and anonymous ids to be the main sources of identity errors over a billion commits, and separate people by behavioural fingerprints such as the files they modify; the default-name lists and the hand-off's shared files below are small pieces of that.
- **GitHub** links commits to accounts by verified email, and by the ID-based noreply address, `ID+USERNAME@users.noreply.github.com`, whose id survives a rename ([GitHub Docs](https://docs.github.com/en/account-and-profile/reference/email-addresses-reference)). The `same-github-account` rule reads that id; a rule over the forge's own commit-to-login links is a follow-up.

## Options

1. **Suggest, with evidence, as mailmap lines for review (chosen).** Nothing is stored or written, so it is a two-way door: rules can change in any release.
2. **Apply confident merges at scan time.** Rejected: an alias table beside `.mailmap` is a second identity store (ADR 0004), and a wrong merge would silently invent a person in every stored number.
3. **Learn identities (ALFAA's approach).** More recall, but it needs labelled data and its reasons are opaque to the person who must sign off a merge. Revisit if the rules miss too much.

The decision is wrong if reviewers often reject high or medium suggestions (tighten the rules), or if people stay split in ways a forge link would have caught (add that rule).

## Rules

A wrong merge invents a person, which is worse than a missed one, so every rule leans to precision, and `identity.Rules` publishes how each can be wrong; every answer carries those definitions.

| Rule | Links | Confidence | How it can be wrong |
| --- | --- | --- | --- |
| `same-name` | Display names (`identity.Directory.Name`) equal once case, spacing and punctuation are ignored: the words, runs of letters, marks and digits, lowercased | medium for two words or more; low for one; high with `same-local-part` | Different people share names, the more often the shorter and commoner the name. A name written without spaces, as Chinese, Japanese and Korean names often are, counts as one word and stays low. A name spelt two ways is missed. |
| `same-local-part` | The same local part on different domains, or one that differs only by a `+tag`; a GitHub noreply address counts as its login (`12345+login@users.noreply.github.com` is `login`) | low alone; high with `same-name` | A first name or a common login as a local part belongs to many people, and git makes up `user@host` addresses from a machine's login name. |
| `same-github-account` | GitHub noreply addresses with the same account id, such as `12345+old@` and `12345+new@` after a rename | high | A forged address: anyone can write any address into a commit. |
| `hand-off` | One address's last commit at most 30 days before another's first, both in a file of the same repository, when the first has no commit in the 90 days before the run's as-of time | low, always, and a guess | A teammate who takes over someone's code when they leave looks exactly like one person changing address. |

A key is no evidence when the facts show it to be several people's:

- **Defaults.** Names a machine or a tutorial fills in (`root`, `Your Name`, `unknown`, `ubuntu`, `(none)` and the like) and role or default local parts (`admin`, `dev`, `info`, `noreply`, `you` and the like) match nothing, nor does a local part shorter than 3 characters.
- **Shared by many.** A name or local part that more than 10 addresses share is a default someone's tooling wrote, not a person's.
- **Two GitHub accounts.** A group holding two noreply account ids links nothing: GitHub's terms allow one person one free personal account, so they are usually two people.
- **Different names.** A local part used under names that differ (neither's words all among the other's, so `ada` and `Ada Example` agree and `Ada Example` and `Ada Lovelace` do not) links nothing. A name that is only the address's own local part, as GitHub names a commit by a login when the account has no profile name, agrees with any name.

Bots and agents (`identity.Directory.Bot`: `[bots]` fragments and `[ai]` agents, ADR 0007) never appear; the answer counts them.

## How candidates form

- **Groups and components.** Each rule groups the addresses by its key (the normalised name, the local part, the account id), and the groups that are evidence are joined where they share an address. Each component is one candidate, so an address is in one candidate at most: git keeps the last of two lines for one alias, silently (verified on 2.50.1), so two candidates must never both map it.
- **Confidence is the weakest pair's.** For each two addresses of a candidate: the same account id, or a full name and a local part together, is high; a full name of two words or more alone is medium; a one-word name or a local part alone is low. Two addresses that share no key at all are linked only through a third (chained), and make the candidate low however strong each link is: chaining multiplies the chances of being wrong.
- **Hand-offs** are looked for only among addresses no other rule links, in time order. An address that stopped long enough ago is compared with each that began within 30 days after its last commit, and a pair needs a file both touched (by a file change of a commit they authored or co-authored, a rename's source included) or hold blamed lines in, in the same repository. A merge's file changes are its first-parent diff, the work of the commits it brought in, so they are not the merger's. An address with two possible successors, or two possible predecessors, gets none, so what is left are chains, each address followed by the one that took over from it. Once a run's other candidates are merged, a later run looks for hand-offs between the merged addresses.
- **Evidence** for each address: its display name, the commits it authored and those whose `Co-authored-by` trailers name it (a SHA counted once across repositories), the first and last author time of those commits, and the repositories it appears in. Commits are the run's history and the older commits blame attributes lines to, which is how an author whose only commit is older than the 365-day history still appears.
- **The canonical address** suggested is, in order: not a GitHub noreply address, which is an alias by design; for a hand-off, the newest, which the person moved to; the one at the domain most of the scope's addresses use, the organisation's convention; then the most commits, the most co-authored, the latest activity and the smallest email. **The suggested name** is the canonical address's when it has two words or more, else the fullest name another address carries, else the canonical address's own; `""`, which keeps each commit's own name, when that is a default.

Candidates are sorted by confidence, guesses after the rest, then by suggested name and first address. They are not versioned like metrics: they are computed when asked, never stored, and only a person acts on them, so a newer binary may suggest differently for the same run.

## Output

`app.Identities(ctx, d, IdentitiesRequest{RunID, Scope})` reads a stored run (the latest unless a run id is given) with its own policy, whose `[bots]` and `[ai]` tables tell bots apart, and never its metric values. Display names come from the whole run, as on every other page; candidates from the scope's repositories (all of them, a group, or one repository by key, scrubbed as `who-knows --repo` is). A scope that names no group or repository of the run is a usage error.

### JSON, and the MCP server

`report.Identities`: `provenance`; `scope`; `addresses` (people) and `bots` (left out); `candidates[]` of `{confidence, guess, commented, chained, rules[], name, addresses[], links[]}`; `rules[]` of `{id, matches, confidence, risk}`; `caveats`. An address is `{email, name, canonical, commits, co_authored, first, last, repos[]}`, with repositories by key. A link is `{rule, addresses[], name, hand_off}`: the indexes it links, the shared display name for `same-name`, and for a hand-off `{gap_days, shared_files, files[]}`, the first five shared files as `{repo, repo_key, path}`. `commented` is true for every low candidate.

Emails appear only in fields named `email`; no other field holds an address or a part of one, which is why a link names no local part or account id. The MCP server serves the value as it is and drops those fields without `--allow-emails`; with it, it can add the suggested lines by rendering `report.Mailmap` over the same value. The caveats are every answer's (`not-fetched`, and the repository caveats: a shallow clone's short history hides addresses), plus `mailmap-at-scan`.

### Text and Markdown name no email

They keep [report-json.md](report-json.md#text-and-markdown)'s rule, although the answer is about emails. A candidate pairs a person's addresses, such as a work address with a personal one, which is the most sensitive fact stillhere prints, and text and Markdown are what people paste into issues and chats. Each address is shown by its domain and the first six hex digits of its SHA-256, as the no-name placeholder is: `…@example.com (b5fc85)` tells two addresses apart and shows a GitHub noreply or a machine's domain, and names no one. Printing emails in this one command's text was the alternative; it would publish them the first time a page is pasted, while the person who edits the mailmap has `--format mailmap` and `--format json`.

### `--format mailmap`

The suggested lines, with the evidence above each candidate's lines as `#` comments. Each line maps one alias to the canonical address and the suggested name, `Name <canonical> <alias>` ([gitmailmap(5)](https://git-scm.com/docs/gitmailmap)), which replaces both for every commit with the alias, whatever its name; a line `Name <canonical>` comes first when the canonical address's own name differs from the suggested one. A high or medium candidate's lines are written as lines; a low candidate's, every guess among them, commented out, so that adding the output to a `.mailmap` unread merges nobody on a weak match. Git's parsing, verified on 2.50.1 with `git check-mailmap`, sets the rest:

- A line is a comment only when `#` is its first character: an indented `# Hank Split <hank@…> <gina@…>` maps `gina@…` to a person named `# Hank Split`. Every comment starts in the first column.
- A `#` inside a name is part of the name, and text after a line's last `>` is ignored; addresses and names match case-insensitively.
- A line whose name or address holds `<` or `>`, a control or format character or a byte that is not UTF-8, or whose name starts with `#`, cannot be the mapping it means, so it is written commented out, escaped, with the reason: its bytes must not reach a terminal either.

The fixtures' suggestions are their hand-written mailmaps byte for byte, and `git check-mailmap` over the printed form maps every uncommented alias to the canonical identity and leaves a guess's alias alone ([SPEC.md](../../testdata/fixture/SPEC.md#identity-candidates-m2-26)).

## Limits

- **No fuzzy names.** Edit distance, nicknames (Bob, Robert) and transliteration (José, Jose) would add recall and false merges; the core has no Unicode normalisation (it imports the standard library only), so a name spelt in two Unicode forms is two names.
- **GitHub only.** Only `users.noreply.github.com`, the documented form, is read as a noreply domain; an Enterprise host's form is unverified (#44).
- **No forge links yet.** The forge replica's commit-to-login links (`ForgeCommit`) are the strongest evidence there is, GitHub's own; a rule over them needs forge facts on the read side (#13).
- **Hand-offs** need the earlier address quiet for 90 days, so a change of address shows up 90 days after the old address's last commit, and only between addresses no other rule links.

## Tests

None of their own ([codebase.md](codebase.md#tests)): the fixture oracles in SPEC.md, which `TestChangeOracles` gains as a row (#13), and, for the rules the fixtures do not hold (noreply logins, account ids, defaults, the share cap, conflicts, chains, ambiguous hand-offs, unsafe lines), the scratch checks recorded in #26's pull request.
