# Validating the ranked metrics

Status: v1 · 2026-10-15 · how to check that a ranked metric measures what it says, on your own repositories: the labelling protocol, the rubric, and the reference result · decision: [ADR 0014](adr/0014-labels-are-evidence-not-the-gate.md) · metrics: [metrics.md](metrics.md#validity-checks)

A ranked metric (`durable@1`, `simplifying@1`, `reviews_given@1`) is a claim: changes that score higher on it are more often the changes that matter. Nothing in the metric's definition proves that. The only test is to compare it with the judgment of someone who knows the code (Kaner and Bond, "Software Engineering Metrics: What Do They Measure and How Do We Know?", METRICS 2004). Until that comparison passes, every rank table, `explain` answer and MCP answer that shows a ranked metric carries a `validity` caveat, and the metric is marked beta.

The bar ([product.md](product.md#how-we-will-know-it-works)): a lead labels at least 50 changes, and the metric separates the meaningful ones from the routine ones with an AUC of at least 0.7, where 0.5 is chance.

## In five minutes

```sh
stillhere scan ~/src/acme          # a stored run (sync github first for reviews_given@1)
stillhere label                    # about 100 changes, one key each; quit and resume at any time
stillhere validate                 # each ranked metric's AUC, with its 95% interval
```

`label` shows one change at a time and takes one key: `m` meaningful, `r` routine, `n` noise, `s` skip, `d` the diff, `?` the rubric, `q` quit. Each answer is written to the labels file at once, so nothing is lost on quit, and running `label` again continues where it stopped. It asks until each ranked metric has 50 changes labelled meaningful or routine among those it scores, the bar `validate` checks: about 100 changes, and one more for each answered noise or skip. It says how far each metric is after every session. Labelling 100 changes takes about half an hour.

To label in a spreadsheet instead: `stillhere label --sheet > ~/.stillhere/sheet.csv` (the directory of the facts database, `$STILLHERE_HOME`, which only you can enter), fill in the `label` column, and `stillhere validate --labels ~/.stillhere/sheet.csv`. Keep the sheet out of every repository: `label` and `validate` warn when the labels file is inside a git work tree. A sheet cannot grow as you answer, so after filling it, `stillhere label --sheet --labels ~/.stillhere/sheet.csv > ~/.stillhere/sheet-2.csv` writes a new one (never redirect onto the sheet it reads) holding your labels and as many more changes as the noise and skip answers left short. `stillhere label --plan` prints the sample, its strata and each metric's progress without asking anything.

Two leads can label the same sample into one file: each runs `stillhere label --labeller NAME`, which writes the name in each row and resumes only that name's rows. `validate` counts a change once, by the label most labellers gave (a tie is left out), and `validate --labeller NAME` scores one lead's labels alone.

## The rubric

Judge the change, not its author and not what you expect stillhere to make of it. Ask: **would a lead want to know this shipped?**

- **meaningful**: a change a lead would want to know shipped. A feature; a real fix of behaviour a user or a caller would notice; a substantive refactor that changes how the code is put together; tests that protect behaviour that was unprotected. Size does not decide it: a ten-line fix of a real bug is meaningful.
- **routine**: necessary but unremarkable. Dependency and version bumps; small configuration, CI or documentation tweaks; mechanical renames and API migrations a tool could do; typo fixes. Size does not decide it either: a thousand-line generated migration is routine.
- **noise**: a change that should not count at all. Formatting (a formatter's run, reflowed comments); generated code committed as is; a revert of one's own change within days, and the change it reverted when it was a temporary experiment; a merge with nothing of its own.
- **skip**: you cannot tell from what you see. A skipped change is not asked about again and is in no AUC.

When torn between two labels, pick the lower one (meaningful over routine only when you would mention it in a summary of the month), and say why in a note if the labels file is shared. Noise is not "small": it is "should not count".

### Worked examples (public repositories)

Every example is a landing on the default branch of a public repository, named by its full SHA; `git show --stat <sha>` shows it (for a merge, `git show --stat --diff-merges=first-parent <sha>`). They were chosen before any metric was scored against them.

Meaningful:

| Repository | Change | What it is | Why |
| --- | --- | --- | --- |
| go-chi/chi | `aba86186a27484f3cadc55b8d6448d2049af71bf` | `fix: panic on Mount() when a router is mounted onto itself` (#1159), 2 files, +45 | A real fix: a crash a caller can trigger, with a test. Small, and still meaningful. |
| go-chi/chi | `3b171578ca44dfd75ca3c5cbddc7b44c600a7b49` | `feat: middleware.ClientIP, a replacement for middleware.RealIP` (#967), 7 files, +1,218 −14 | A feature that replaces an API with a safer one, with tests and documentation. |
| cli/cli | `1957a022b63b8e093198d8f03dbf7fd8bff77f66` | the merge of #12652, "Fix redundant API call in gh issue view --comments", 3 files, +11 −10 | A real fix a user notices (one API call fewer per command). Size does not decide. |
| cli/cli | `2728635b43542c098623b54f379e2e7d450c56df` | the merge of #12444, a native `copilot` command, 8 files, +1,163 −7 | A feature, most of it tests. |

Routine:

| Repository | Change | What it is | Why |
| --- | --- | --- | --- |
| go-chi/chi | `756fcb8d630fc10c53e2cc0f824c3b53112a6cba` | `Bump minimum Go to 1.24, use b.Loop()` (#1164), 6 files, +7 −12 | A version bump and the mechanical follow-up it allows. |
| go-chi/chi | `50ef4e4c311ff0c31e12e97e4e9f186b4cd0758d` | `Replace "interface{}" with "any"` (#1167), 8 files, +14 −14 | A mechanical rename a tool does. |
| go-chi/chi | `f975af0cb10cbefaccf0422385420fe62722d648` | `Fix typo in Route doc comment` (#1073), +1 −1 | A documentation tweak. |
| cli/cli | `3963b735b757b45d083f43ebac97b67821d0c946` | `chore: bump go to 1.26.7` (#14205), `go.mod` +1 −1 | A toolchain bump. |

Noise:

| Repository | Change | What it is | Why |
| --- | --- | --- | --- |
| go-chi/chi | `992028394fe1872355469942c81363200626623e` | `Reformat godoc comments according to the latest Go` (#867), 14 files, +115 −143 | Formatting: gofmt's new comment layout, no behaviour. It belongs in `.git-blame-ignore-revs`. |
| cli/cli | `0cf1092493af067646fc5f3db9421c6a6ec9c938` | the merge of #14462, which reverts #14437 (`38316c1c4f275030e3df6666382922e75410d68b`) a day after it landed, 2 files, +3 −3 | A revert of the team's own change within days: the pair together did nothing. |
| cli/cli | `856ad9dfa9c2b10f9f322e69a476b48e45364f99` | the merge of #14203, which reverts a "temporary step" added to the deployment workflow the same day, −28 | A temporary experiment and its removal: neither should count. |

Hard cases, and how the rubric settles them:

- **Large, and mostly deletion.** cli/cli `7ea88b1c4d99ce0abc6055277d266b27381d74f7`, `Bundle licenses at release time` (#12625): 205 files, +318 −17,385, nearly all of it third-party license texts that were committed and are now generated at release. It changes how every release is built, so **meaningful**; the deleted lines are not what makes it so.
- **A small CI change with consequences.** go-chi/chi `8b258c7bb28f97a5f2a856ff7ef962578fec9215`, `ci: pin GitHub Actions to full commit SHAs` (#1116), +2 −2: supply-chain hardening a lead may want to hear about, but it is two lines of configuration that follow a common recommendation. **Routine**, unless your team made it a goal.
- **A dependency bump that fixes a vulnerability** is still routine; the vulnerability fix happened elsewhere. Label the bump that needed code changes to adopt the new version by those changes.

## What `stillhere label` asks about

### The sample

`label` draws from a stored run (the latest, or `--run ID`) the changes that a ranked metric scores at its own grain ([below](#how-validate-scores-a-change)), people's only: a bot's changes are never ranked, so none is asked about. A change is a landing, or a pull request with the landings that put it on the default branch when the run's GitHub data has it.

The sample is stratified (Cochran, *Sampling Techniques*, 3rd ed., 1977, ch. 5):

- **By window.** Each ranked metric scores the changes of its own window: `durable@1` those that landed 90 to 365 days ago, `simplifying@1` and `reviews_given@1` those of the last 90 days. Each window gets its own part of the sample. By default a window's part runs until each of its metrics scores 50 of its changes that are not answered noise or skip, the 50 the bar needs, and at most 100: about 50 per window, more where `reviews_given@1` cannot score every recent change (a direct push is no pull request) or where answers were noise or skip. `--n N` asks for N in all instead, the windows taking turns.
- **Within a window, by repository and size, in proportion.** The sample's mix of repositories and of small (fewer than 10 lines added and removed on paths with a role), medium (10 to 99) and large (100 or more) changes is the window's own, so the AUC describes the changes the ranks are made of. `--plan` prints the strata.

Each window's changes are one sequence, and a window's part of the sample is a prefix of it: each next change comes from the stratum furthest below its share (the Sainte-Laguë divisors 1, 3, 5, …). So a larger sample keeps every label of a smaller one and adds to it, and a labels file made with one sample still counts in full whatever sample comes next. The same run, `--n`, `--seed` and labels give the same sample on every machine; within a stratum, changes come in the order of a hash of the seed and their SHA.

On cli/cli at `--now 2026-10-15` (run of the dogfood, partial GitHub data), 292 changes qualify, and `--n 20` is drawn like this:

| Window | Scored by | Size | Pool | Sample |
| --- | --- | --- | --- | --- |
| 90–365 days ago | durable@1 | small | 23 | 2 |
| 90–365 days ago | durable@1 | medium | 64 | 4 |
| 90–365 days ago | durable@1 | large | 61 | 4 |
| 0–90 days ago | simplifying@1, reviews_given@1 | small | 73 | 5 |
| 0–90 days ago | simplifying@1, reviews_given@1 | medium | 34 | 2 |
| 0–90 days ago | simplifying@1, reviews_given@1 | large | 37 | 3 |

### Blinding

A card shows the repository, the landing date, the window, the size, the pull request's number and title (or the landing's subject), the subjects of the commits a merge brought in or, for a rebase merge, of its commits, each a landing of its own (oldest first, at most 10, leaving out one whose subject is the title), and the files with their lines and class; a rebase merge's lines are each landing's own, summed, which can count a line one landing adds and a later one removes twice. `d` adds the patch of the files with a role, of the whole change: a rebase merge's several landings from the parent of the oldest, not its newest landing alone, cut at 24,000 bytes like any patch. It never shows a metric value, a rank or an author: a label that knew the metric's verdict would measure agreement with the metric, and one that knew the author would judge the person. The login in GitHub's merge subject ("Merge pull request #950 from owner/branch") is removed for the same reason, and the card shows subjects, never a commit's message body, so no `Signed-off-by` or `Co-authored-by` trailer reaches it. A subject that names its author some other way still does, and so does a patch (a `Cargo.toml` `authors` line, say).

### The labels file

CSV, one change per row: change, label, and optionally the labeller.

```
change,label,labeller
1957a022b63b8e093198d8f03dbf7fd8bff77f66,meaningful,lead
#950,routine,lead
pr:951,noise,lead
cli/cli#952,meaningful,lead
5ac7a82,skip,lead
```

- A **change** is a landing's SHA, or at least 7 of its first hex digits when no other landing of the run starts with them; or a **pull request** that landed in the run's history, written `#950`, `pr:950` or `owner/name#950`. A bare number names the pull request of that number in whichever of the run's repositories has one, and is left out when two do.
- A **label** is `meaningful`, `routine` or `noise`. `skip`, or an empty label (a sheet's row not filled in yet), is no label.
- A **labeller** is the third field, optional. A change counts once per labeller, by that labeller's last row for it, however the rows name it; `label --labeller NAME` resumes only NAME's rows, and rows with no labeller are one unnamed labeller's.
- A line that starts with `#` is a comment, unless it names a pull request as `#` and its digits (`#950,meaningful`). A first row that names no change and gives no label is a header.
- The file judges colleagues' work. By default it is `labels.csv` beside the facts database (`$STILLHERE_HOME/labels.csv`), outside every repository, created 0600. Do not commit it.

## How validate scores a change

`stillhere validate` reads the labels and a stored run, and writes nothing. Each labelled change gets each ranked metric's **measure of that change**: what the change adds to the metric's clusters, the same units the ranks are made of (`metrics.Scorer`). A metric that does not score a change leaves it out of that metric's AUC; `n` says how many it scored.

| Metric | Its measure of a change | Scores |
| --- | --- | --- |
| `durable@1` | Σ surviving / Σ added over the change's clusters: every person's counted commits in each of its landings | a change that landed 90 to 365 days ago and added a `prod` line whose fate blame can tell |
| `simplifying@1` | Σ removed `prod` lines over the change's clusters | a change of the last 90 days with a counted commit, its revert pairs left out |
| `reviews_given@1` | the people other than its author, bots aside, who submitted a review of its pull request at any time before the run's now: the reviewers its clusters count, but by the change's window, not the reviews' (a pull request merged early in the window keeps the reviews it got before) | a pull request of the run's GitHub data that landed in the last 90 days; one no one else reviewed scores 0. A landing no pull request of the data landed is not scored: a direct push, a merge GitHub's API does not list (a security advisory's "Merge commit from fork") and a pull request the sync missed look alike, so a 0 would read a gap as no review |

Only a change that landed in the metric's window is scored. A change counts once: each labeller's last row for it, and over several labellers the label most of them gave; a tie is left out, and a caveat says how many changes several labellers labelled and how often they agreed. When a metric scored fewer of the labelled changes of its window than were labelled, a caveat says how many, and what it scores; when it scored none and it reads GitHub data, the caveat says whether the run has any.

`validate --scores > ~/.stillhere/scores.csv` writes each labelled change with its label, its size (the lines it added and removed on paths with a role) and each metric's measure of it, empty where the metric does not score it: the very numbers each AUC is computed from, so anyone can recompute one, or look for a size confound on exactly the changes a metric scored. `--format json` adds them to the answer as `scores`. The file holds the labels of colleagues' changes, so keep it beside the labels, out of your repositories.

Then, per metric, the AUC: the chance that a change labelled meaningful scores above one labelled routine, a tie counting one half (Mann-Whitney; `rank.AUC`). Noise is left out of every AUC: it is the class the noise rules already strip, and it says nothing about the order of the rest. The interval in parentheses is a 95% bootstrap interval: 1,000 replicates that resample the meaningful and the routine changes apart, so each keeps the sample's counts (`rank.AUCInterval`); it depends on the scores alone, not on the order of the file's rows. At 50 labels an AUC's interval is about ±0.15 wide, so a 0.72 does not yet say the metric beats 0.6.

The status column says whether the labels clear the bar (AUC ≥ 0.7 on at least 50 scored labels), or what is short. Under the table, each metric's verdict says what its numbers mean, in the words of a Validity line, by the rule [metrics.md](metrics.md#validity-checks) states: whether it separates meaningful from routine changes, and whether it beats change size; the bar does not ask the second. Labels that clear it are evidence about the code they judge, whoever made them: the model panel's majority labels, run through `validate`, print "clears the bar" too. A metric loses its beta mark only on a lead's labels ([ADR 0014](adr/0014-labels-are-evidence-not-the-gate.md)), and only in its published Validity line: the mark in stillhere's output is the metric's, not your installation's, so a lead whose labels clear the bar changes nothing it prints, and can report the result (an issue with the AUC, its interval, n and the kind of codebase) for the next release to weigh.

Read an AUC beside the size of the changes: big changes are more often meaningful, and a metric that grows with size (`simplifying@1` counts removed lines) can separate the classes through size alone. So beside each metric's AUC, `validate` prints **size alone**: the AUC of change size (lines added and removed on paths with a role) over the same changes the metric scored, with its interval from the same bootstrap. A metric worth ranking by beats it, which is a question about one sample: the JSON's `size_diff_lo` and `size_diff_hi` are the 95% interval of the metric's AUC less size alone's, each bootstrap replicate resampling the changes once and taking both AUCs over them (`rank.AUCDiffInterval`, the paired comparison of DeLong, DeLong and Clarke-Pearson, Biometrics 1988), and the verdict says "beyond change size" only when that interval lies above 0.

## Reference validation: a model panel

A model panel is three AI models that label public changes by this page's rubric, blinded to every metric value, to each other and, as far as the evidence allows, to who made each change (the Evidence gaps below say what leaked in its first run). A lead's labels are the gate. Before any lead has labelled, stillhere publishes the panel's result as a reference so that someone trying it has evidence about the ranks, and so that the protocol is exercised end to end. It is evidence, not the gate, and no AI judgment ever ships in stillhere's outputs ([product.md](product.md#non-goals-v1)).

- **Repositories**: three or four public repositories in different languages, each synced so `reviews_given@1` can score its changes.
- **Sample**: `stillhere label --sheet`, about 38 changes per repository, about 150 in all, at one fixed `--now` and seed.
- **Raters**: three model raters, each in a fresh session that sees only this page's rubric, one sheet row, and the stat and patch of the whole change (for a rebase merge, from the parent of its oldest landing), with no author line, no message body (its trailers name people) and no login in a merge subject: what `label`'s card and `d` show; blinded to every metric value and to each other. A sheet row holds the pull request's title and the commits' subjects, which stillhere never sends to an AI (`metrics.Pull.Title` and `forge_snapshot_pulls.title` are "local only"). The panel is the one exception, and only because of where it runs: the maintainers run it, outside stillhere, on public repositories whose titles and patches anyone can read; stillhere's own output never goes to a model, and the rule stands for every user's repositories.
- **Agreement**: Krippendorff's alpha, nominal, over the three raters, skips as missing values (Krippendorff, "Computing Krippendorff's Alpha-Reliability", 2011). Below 0.667 the result is published as "raters disagree", with no AUC claim.
- **Labels**: the majority label of each change (two of three); a change with no majority is left out.
- **Scores**: `stillhere validate` on the majority labels, per repository and pooled, each AUC with its interval and n, beside its size alone; and `validate --scores` for an analysis of size outside stillhere.

Why models, and why only as reference: model annotation can match or beat crowd workers on text classification (Gilardi, Alizadeh and Kubli, "ChatGPT outperforms crowd workers for text-annotation tasks", PNAS 2023), but its quality varies by task and must be checked against human labels task by task (Pangakis, Wolken and Fasching, "Automated Annotation with Generative AI Requires Validation", 2023). Labels of software changes are hard for people too: Herzig, Just and Zeller found a third of the bug reports they reviewed misclassified ("It's Not a Bug, It's a Feature", ICSE 2013), which is why labels here follow a written rubric with worked examples. A panel of three sessions of one model family shares that family's biases, so its alpha overstates the agreement independent people would reach.

### Reference results

The panel labelled four public repositories as they stood at `--now 2026-10-15`, the date its result goes by: cli/cli (Go), pallets/flask (Python), BurntSushi/ripgrep (Rust) and vitejs/vite (TypeScript), scanned by stillhere at commit eb77769; the raters worked on 2026-10-16 UTC. Each repository's own `STILLHERE_HOME` gave its sheet and its rows below; one more home that scanned the four mirrors together (the same heads, landings and pull requests) gave the pooled rows. Its GitHub data is as of 2026-10-16 03:08 UTC (the single homes' 03:01), with no pull request missing.

| Repository | Branch at `--now` | Head |
| --- | --- | --- |
| cli/cli | `trunk` | `ff67d7622da2503223b8437e8cfaa39e1f6bf0e2` |
| pallets/flask | `main` | `d086db856be187255b8ec61ef409357393020f32` |
| BurntSushi/ripgrep | `master` | `3fce3b5bb0236da2df6d99672afb8a719642eca7` |
| vitejs/vite | `main` | `c2c9ef4dcd05b3e4d88844ea3a13dc23819ca2a5` |

- **Sample**: `label --sheet --n 38 --seed 20261015`, 143 changes: 38 of cli's 290 scored changes, 38 of vite's 674, and every change flask's and ripgrep's runs score (29 and 38).
- **Raters**: `claude-opus-5-5`, `claude-sonnet-5-5` and `claude-fable-5-1`, one session per rater and repository. Their transcripts show no read of another rater's labels, of stillhere's output or of git's author fields.
- **Agreement**: Krippendorff's alpha 0.851 pooled, with pairwise agreement of 91%, 95% and 92%. Per repository it is cli 0.791, flask 1.000, ripgrep 0.771 and vite 0.865. Every alpha is above 0.667, so the AUCs are published. cli's and ripgrep's are below 0.800, so their rows are tentative by Krippendorff's bar.
- **Majority**: every change has one: 61 meaningful, 80 routine and 2 noise.
- **Size alone**: `validate`'s column, the AUC of the lines a change added and removed on paths with a role, over the changes the metric scored, with the metric's own bootstrap. Over all 141 changes labelled meaningful or routine it is 0.84.
- **Policy**: every run above used the packaged policy's revision 2 (policy hash `39f300454d59f2e104446379e1a827948728b146350f6d207d6d7f5f6deb48ba`, as `validate` and every page print it, `39f300454d59`), the default when the panel ran. [Revision 3](spec/classification.md#what-counts-as-product-code-revision-3) (hash `bdbfe2296b0f…`), the default since, takes example code, benchmarks, documentation and build tooling out of `prod` and gives CI and build configuration a role. The numbers below are revision 2's, and the Validity lines quote them. The same pooled home's four mirrors, scanned again at the same `--now` under revision 3 and scored against the same majority labels, give the same verdict for each metric: `durable@1` 0.43 (0.30–0.55) on 74 (two changes fewer: a flask merge whose only prod lines were under `examples/`, and a vite change to its documentation site under `docs/`, both now `doc`), with size alone 0.82; `simplifying@1` 0.77 (0.63–0.88) on 65, with size alone 0.81 (0.70–0.91), paired -0.05 (-0.17 to 0.09); `reviews_given@1` 0.55 (0.40–0.71) on 49, with size alone 0.82. Size alone moves most, since a change's lines on CI and build files now count in its size.

| Metric | Repositories | n (meaningful / routine) | AUC (95%) | Size alone (95%) | Alpha | Raters | Date · `--now` · head |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `durable@1` | all 4 | 76 (40 / 36) | 0.42 (0.30–0.55) | 0.82 (0.70–0.90) | 0.851 | opus-5-5, sonnet-5-5, fable-5-1 | 2026-10-15 · 2026-10-15 · the four above |
| `durable@1` | cli | 19 (12 / 7) | 0.38 (0.12–0.64) | 0.93 (0.76–1.00) | 0.791 | the same | 2026-10-15 · 2026-10-15 · ff67d76 |
| `durable@1` | flask | 16 (9 / 7) | 0.56 (0.28–0.81) | 0.87 (0.65–1.00) | 1.000 | the same | 2026-10-15 · 2026-10-15 · d086db8 |
| `durable@1` | ripgrep | 22 (7 / 15) | 0.30 (0.09–0.52) | 0.86 (0.66–0.99) | 0.771 | the same | 2026-10-15 · 2026-10-15 · 3fce3b5 |
| `durable@1` | vite | 19 (12 / 7) | 0.50 (0.26–0.74) | 0.54 (0.25–0.83) | 0.865 | the same | 2026-10-15 · 2026-10-15 · c2c9ef4 |
| `simplifying@1` | all 4 | 65 (21 / 44) | 0.76 (0.63–0.88) | 0.87 (0.76–0.95) | 0.851 | opus-5-5, sonnet-5-5, fable-5-1 | 2026-10-15 · 2026-10-15 · the four above |
| `simplifying@1` | cli | 19 (7 / 12) | 0.90 (0.74–1.00) | 0.96 (0.86–1.00) | 0.791 | the same | 2026-10-15 · 2026-10-15 · ff67d76 |
| `simplifying@1` | flask | 13 (3 / 10) | 0.43 (0.08–0.80) | 0.47 (0.10–0.83) | 1.000 | the same | 2026-10-15 · 2026-10-15 · d086db8 |
| `simplifying@1` | ripgrep | 15 (6 / 9) | 0.92 (0.75–1.00) | 0.96 (0.85–1.00) | 0.771 | the same | 2026-10-15 · 2026-10-15 · 3fce3b5 |
| `simplifying@1` | vite | 18 (5 / 13) | 0.64 (0.37–0.91) | 0.77 (0.54–0.97) | 0.865 | the same | 2026-10-15 · 2026-10-15 · c2c9ef4 |
| `reviews_given@1` | all 4 | 49 (19 / 30) | 0.55 (0.40–0.71) | 0.89 (0.78–0.96) | 0.851 | opus-5-5, sonnet-5-5, fable-5-1 | 2026-10-15 · 2026-10-15 · the four above |
| `reviews_given@1` | cli | 18 (6 / 12) | 0.69 (0.50–0.88) | 0.97 (0.89–1.00) | 0.791 | the same | 2026-10-15 · 2026-10-15 · ff67d76 |
| `reviews_given@1` | flask | 3 (2 / 1) | 0.75 (0.50–1.00) | 0.00 (0.00–0.00) | 1.000 | the same | 2026-10-15 · 2026-10-15 · d086db8 |
| `reviews_given@1` | ripgrep | 10 (6 / 4) | 0.38 (0.04–0.71) | 0.92 (0.67–1.00) | 0.771 | the same | 2026-10-15 · 2026-10-15 · 3fce3b5 |
| `reviews_given@1` | vite | 18 (5 / 13) | 0.72 (0.58–0.88) | 0.77 (0.54–0.95) | 0.865 | the same | 2026-10-15 · 2026-10-15 · c2c9ef4 |

`validate` prints "clears the bar" for one row only: `simplifying@1` pooled, and under it the verdict "separates, but no better than change size". Pooled `durable@1` is below the AUC of 0.7, and pooled `reviews_given@1` is short of n 50. Every repository row is short of n 50.

Each rater's own labels, run through `validate` pooled, give AUCs within 0.07 of the majority's. Those AUCs do not depend on the majority vote:

| Metric | Majority | `claude-opus-5-5` | `claude-sonnet-5-5` | `claude-fable-5-1` |
| --- | --- | --- | --- | --- |
| `durable@1` | 0.42 (0.30–0.55), 40 / 36 | 0.40 (0.28–0.53), 43 / 33 | 0.45 (0.32–0.57), 39 / 37 | 0.42 (0.30–0.55), 40 / 36 |
| `simplifying@1` | 0.76 (0.63–0.88), 21 / 44 | 0.74 (0.61–0.86), 22 / 43 | 0.70 (0.56–0.83), 17 / 50 | 0.74 (0.61–0.86), 22 / 43 |
| `reviews_given@1` | 0.55 (0.40–0.71), 19 / 30 | 0.52 (0.37–0.68), 20 / 29 | 0.61 (0.46–0.76), 15 / 36 | 0.56 (0.41–0.71), 20 / 29 |

What it says, per metric, in the words of its Validity line ([metrics.md](metrics.md#validity-checks) gives the rule that picks them):

- **`durable@1` does not separate meaningful from routine changes.** Its AUC is 0.42 (0.30–0.55) on 76, and 0.40 to 0.45 for each rater. Change size alone scores 0.82 (0.70–0.90) on the same changes; paired, the metric is 0.39 below it (0.23 to 0.55 below). If anything, the changes the panel called meaningful kept fewer of their lines than the routine ones. One possible reason, not tested here: a large change touches code that is still being worked on, while a one-line fix rarely gets rewritten.
- **`simplifying@1` separates, but no better than change size.** Its AUC is 0.76 (0.63–0.88) on 65, and 0.70 to 0.74 for each rater. The lower bound, 0.63, is below 0.7. Change size alone scores 0.87 (0.76–0.95) on the same changes; paired, the difference is -0.11 (-0.22 to 0.01), which holds 0, and in every repository the metric is below that repository's size alone. So the panel shows that it ranks big changes high, which its definition predicts (it counts removed lines). The panel does not show that it says more than size.
- **`reviews_given@1`: cannot tell yet.** It has 49 scored labels, one short of the bar's 50, and its AUC of 0.55 (0.40–0.71) spans chance. The raters give 0.52 to 0.61. 16 of the 65 recent changes labelled meaningful or routine are no pull request of the GitHub data (flask's 10: 8 direct pushes and 2 merges of its `stable` branch, pushed to the branch; ripgrep's 5 release commits; and cli's 1, a security advisory's "Merge commit from fork", which GitHub's API does not list), so the metric does not score them.

**Beyond size.** An AUC beside size alone cannot say whether a metric adds anything once size is known, so the per-change scores (`validate --scores` on the pooled majority labels) were analysed outside stillhere for the two metrics with enough labels. Within label's size strata (small, medium and large), `durable@1`'s AUC is 0.38, 0.55 and 0.28 (25, 31 and 20 changes), and 0.48 (0.35–0.61) over the 390 pairs of a meaningful and a routine change of one stratum; `simplifying@1`'s is 0.90, 0.55 and 0.54 (30, 23 and 12 changes; the small stratum holds one meaningful change), and 0.60 (0.43–0.78) over 188 such pairs. A logistic regression of meaningful against routine on log(1 + size) gains nothing from either score: `durable@1`'s coefficient is −0.47 (95% −2.02 to 1.08; likelihood-ratio χ² 0.36, p 0.55), and `simplifying@1`'s, on log(1 + removed `prod` lines), −0.13 (−0.81 to 0.54; χ² 0.15, p 0.70; on the raw count, p 0.94), while log size alone has a coefficient of 0.88 (SE 0.22) and 0.84 (SE 0.21) in the two samples. So on these labels neither metric says anything about which changes mattered beyond their size. That is evidence about these 76 and 65 changes, not a new definition: no metric is changed on the sample that tests it, as no rubric example was chosen after a metric was scored.

No metric loses "beta" on this ([ADR 0014](adr/0014-labels-are-evidence-not-the-gate.md)). The same numbers from a lead would clear the bar for `simplifying@1` as product.md and ADR 0014 set it (AUC ≥ 0.7 on 50), and `validate` would say "clears the bar" beside the verdict "separates, but no better than change size". Whether beating size alone should be part of the bar is the owner's decision, open in [ADR 0014's amendment](adr/0014-labels-are-evidence-not-the-gate.md#amendment-the-published-form-and-size-alone-2026-10-16); until it is made, we recommend weighing such a result by its verdict, as evidence that the metric ranks big changes high, not that it says more than size.

What could make this result wrong, beyond the limits above:

- **One vendor.** The three raters are three models of one vendor, so their alpha overstates what independent people would reach. flask's 1.000 on 29 changes is the extreme case.
- **Raters see size.** The row gives the line counts and the patch has its length, so part of size alone's AUC may be the raters reading size. Then size is a harder bar to beat than it would be with labels made without it. A lead sees the same evidence.
- **Departures from the protocol.** Each rater labelled a whole sheet in one session and ran the evidence script itself, instead of answering one prompt per row, so it saw the sheet's other rows. `claude-sonnet-5-5` read the patches in batches, with lines cut at 150 to 220 characters.
- **Evidence gaps.** The raters read each change through `git show` of its first landing, not through `label`'s card, and that leaked where the card does not:
  - four of ripgrep's rebase-merged changes showed the patch of their newest landing only; the worst, 16 landings, showed 130 of 894 lines (`label`'s `d` now diffs the whole change from the oldest landing's parent, cut at 24,000 bytes like any patch: 9 of that change's 18 files);
  - 31 of cli's 38 commit subjects are "Merge pull request #N from <account>/<branch>", whose login the card removes;
  - 16 rows (6 of cli's, 10 of vite's) carry identity trailers such as `Signed-off-by` and `Co-authored-by` in their message body, which the card never shows;
  - 6 ripgrep patches show a Cargo.toml `authors` line, which a patch cannot hide.
- **The GitHub data is about 27 hours newer than `--now`.** A review given in those hours counts for `reviews_given@1`. The sync's history also starts about 27 hours after the scan's, so two ripgrep changes at the old end of `durable@1`'s window, which landed in those hours, have no pull request in the data, and their sheet title is the commit subject.
- **Small n.** Per repository, n is 3 to 22. Those intervals are 0.25 to 0.72 wide, and no repository row says much alone. flask's `reviews_given@1` row (n 3) says nothing.

The sheets, the raters' labels and the scripts that aggregated and analysed them stay outside the repository ([ADR 0014](adr/0014-labels-are-evidence-not-the-gate.md)). The sample is reproducible from the run, `--n`, `--seed` and `--now` above; a sheet written by a later stillhere lists each landing's subject for the four rebase-merged rows, which the panel's sheet did not.

## Limits

- **One lead is one view.** Two labellers who disagree on a third of the changes would be normal for this task (Herzig et al.). Two leads can label the same sample into one file (`label --labeller NAME`); `validate` pools them by majority and says how often they agreed, and `validate --labeller NAME`, once per lead, shows whether the AUC holds across them.
- **The AUC describes the sample.** Within a window it is the window's own mix; across windows, each metric is scored only on its own window's half, so the two halves never mix in one AUC.
- **A change is the grain of the labels, not of every metric.** `reviews_given@1` ranks reviewers by the pull requests they reviewed; its validity check asks whether review goes to the changes that matter, which is the property its ranks reward.
- **What the card cannot hide.** A subject or a patch can name its author; a lead who recognises the code may know who wrote it.
- **Missing GitHub data is never read as no review.** `reviews_given@1` scores only the pull requests the run's GitHub data holds, so a sync that missed pull requests leaves their changes unscored (fewer labels for its bar, which the caveats count), not scored 0. What it cannot see is a review missing from a pull request the data holds; and a repository whose changes are mostly direct pushes gives it few changes to score.
- **Rebase merges.** A pull request merged by rebase is one change when its landings share the merge's committer second ([metrics.md](metrics.md#who_knows1-per-path)); its card lists each landing whose subject is not the title, with lines summed over its landings, and `d` shows the whole change, from the oldest one's parent, cut at 24,000 bytes like any patch.
