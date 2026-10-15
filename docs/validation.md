# Validating the ranked metrics

Status: v1 · 2026-10-15 · how to check that a ranked metric measures what it says, on your own repositories: the labelling protocol, the rubric, and the reference result · decision: [ADR 0014](adr/0014-labels-are-evidence-not-the-gate.md) · metrics: [metrics.md](metrics.md#validity-checks)

A ranked metric (`durable@1`, `simplifying@1`, `reviews_given@1`) is a claim: changes that score higher on it are more often the changes that matter. Nothing in the metric's definition proves that. The only test is to compare it with the judgment of someone who knows the code (Kaner and Bond, "Software Engineering Metrics: What Do They Measure and How Do We Know?", METRICS 2004). Until that comparison passes, every rank table, `explain` answer and MCP answer that shows a ranked metric carries a `validity` caveat, and the metric is marked beta.

The bar ([product.md](product.md#how-we-will-know-it-works)): a lead labels at least 50 changes, and the metric separates the meaningful ones from the routine ones with an AUC of at least 0.7, where 0.5 is chance.

## In five minutes

```sh
stillhere scan ~/src/acme          # a stored run (sync github first for reviews_given@1)
stillhere label                    # 100 changes, one key each; quit and resume at any time
stillhere validate                 # each ranked metric's AUC, with its 95% interval
```

`label` shows one change at a time and takes one key: `m` meaningful, `r` routine, `n` noise, `s` skip, `d` the diff, `?` the rubric, `q` quit. Each answer is written to the labels file at once, so nothing is lost on quit, and running `label` again continues where it stopped. Labelling 100 changes takes about half an hour.

To label in a spreadsheet instead, or to split the work: `stillhere label --sheet > sheet.csv`, fill in the `label` column, and `stillhere validate --labels sheet.csv`. `stillhere label --plan` prints the sample and its strata without asking anything.

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

- **By window, equally.** Each ranked metric scores the changes of its own window: `durable@1` those that landed 90 to 365 days ago, `simplifying@1` and `reviews_given@1` those of the last 90 days. Each window gets half of the sample, so each metric gets about half the labels; the default of 100 gives each the 50 the bar needs.
- **Within a window, by repository and size, in proportion.** The sample's mix of repositories and of small (fewer than 10 lines added and removed on paths with a role), medium (10 to 99) and large (100 or more) changes is the window's own, so the AUC describes the changes the ranks are made of. `--plan` prints the strata.

The sample is one sequence, and `--n` takes a prefix of it: within a window each next change comes from the stratum furthest below its share (the Sainte-Laguë divisors 1, 3, 5, …), and the windows take turns. Asking for 150 after labelling 100 keeps every label and adds 50. The same run, `--n` and `--seed` give the same sample on every machine; within a stratum, changes come in the order of a hash of the seed and their SHA.

On cli/cli at `--now 2026-10-15` (run of the dogfood, partial GitHub data), 292 changes qualify, and a sample of 20 is drawn like this:

| Window | Scored by | Size | Pool | Sample |
| --- | --- | --- | --- | --- |
| 90–365 days ago | durable@1 | small | 23 | 2 |
| 90–365 days ago | durable@1 | medium | 64 | 4 |
| 90–365 days ago | durable@1 | large | 61 | 4 |
| 0–90 days ago | simplifying@1, reviews_given@1 | small | 73 | 5 |
| 0–90 days ago | simplifying@1, reviews_given@1 | medium | 34 | 2 |
| 0–90 days ago | simplifying@1, reviews_given@1 | large | 37 | 3 |

### Blinding

A card shows the repository, the landing date, the window, the size, the pull request's number and title (or the landing's subject), the subjects of the commits a merge brought in, and the files with their lines and class. `d` adds the patch of the files with a role. It never shows a metric value, a rank or an author: a label that knew the metric's verdict would measure agreement with the metric, and one that knew the author would judge the person. The login in GitHub's merge subject ("Merge pull request #950 from owner/branch") is removed for the same reason. A subject that names its author some other way still does, and so does a patch.

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
- A line that starts with `#` is a comment, unless it names a pull request as `#` and its digits (`#950,meaningful`). A first row that names no change and gives no label is a header.
- The file judges colleagues' work. By default it is `labels.csv` beside the facts database (`$STILLHERE_HOME/labels.csv`), outside every repository, created 0600. Do not commit it.

## How validate scores a change

`stillhere validate` reads the labels and a stored run, and writes nothing. Each labelled change gets each ranked metric's **measure of that change**: what the change adds to the metric's clusters, the same units the ranks are made of (`metrics.Scorer`). A metric that does not score a change leaves it out of that metric's AUC; `n` says how many it scored.

| Metric | Its measure of a change | Scores |
| --- | --- | --- |
| `durable@1` | Σ surviving / Σ added over the change's clusters: every person's counted commits in each of its landings | a change that landed 90 to 365 days ago and added a `prod` line whose fate blame can tell |
| `simplifying@1` | Σ removed `prod` lines over the change's clusters | a change of the last 90 days with a counted commit, its revert pairs left out |
| `reviews_given@1` | the people other than its author who submitted a review of its pull request in the window: its clusters, one per reviewer | a change of the last 90 days in a repository whose GitHub data the run read; a change no one else reviewed, or that no pull request landed (a direct push), scores 0 |

Then, per metric, the AUC: the chance that a change labelled meaningful scores above one labelled routine, a tie counting one half (Mann-Whitney; `rank.AUC`). Noise is left out of every AUC: it is the class the noise rules already strip, and it says nothing about the order of the rest. The interval in parentheses is a 95% bootstrap interval: 1,000 replicates that resample the meaningful and the routine labels apart, so each keeps the sample's counts (`rank.AUCInterval`). At 50 labels an AUC's interval is about ±0.15 wide, so a 0.72 does not yet say the metric beats 0.6.

A metric loses its beta mark only when its AUC is at least 0.7 on at least 50 scored labels **from a lead** ([ADR 0014](adr/0014-labels-are-evidence-not-the-gate.md)).

Read an AUC beside the size of the changes: big changes are more often meaningful, and a metric that grows with size (`simplifying@1` counts removed lines) can separate the classes through size alone. The sheet's `added` and `removed` columns give the AUC of plain size on the same labels; a metric worth ranking by beats it.

## Reference validation: a model panel

A lead's labels are the gate. Before any lead has labelled, stillhere publishes a reference result so that someone trying it has evidence about the ranks, and so that the protocol is exercised end to end. It is evidence, not the gate, and no AI judgment ever ships in stillhere's outputs ([product.md](product.md#non-goals-v1)).

- **Repositories**: three or four public repositories in different languages, each synced so `reviews_given@1` can score its changes.
- **Sample**: `stillhere label --sheet`, about 38 changes per repository, about 150 in all, at one fixed `--now` and seed.
- **Raters**: three model raters, each in a fresh session that sees only this page's rubric, one sheet row, and `git show --stat` and `--patch` of the change printed without author lines; blinded to every metric value and to each other.
- **Agreement**: Krippendorff's alpha, nominal, over the three raters, skips as missing values (Krippendorff, "Computing Krippendorff's Alpha-Reliability", 2011). Below 0.667 the result is published as "raters disagree", with no AUC claim.
- **Labels**: the majority label of each change (two of three); a change with no majority is left out.
- **Scores**: `stillhere validate` on the majority labels, per repository and pooled, each AUC with its interval and n, beside the size baseline.

Why models, and why only as reference: model annotation can match or beat crowd workers on text classification (Gilardi, Alizadeh and Kubli, "ChatGPT outperforms crowd workers for text-annotation tasks", PNAS 2023), but its quality varies by task and must be checked against human labels task by task (Pangakis, Wolken and Fasching, "Automated Annotation with Generative AI Requires Validation", 2023). Labels of software changes are hard for people too: Herzig, Just and Zeller found a third of the bug reports they reviewed misclassified ("It's Not a Bug, It's a Feature", ICSE 2013), which is why labels here follow a written rubric with worked examples. A panel of three sessions of one model family shares that family's biases, so its alpha overstates the agreement independent people would reach.

### Reference results

Not run yet. The panel runs after the current wave's code is integrated; its results go here, one row per metric and repository plus the pooled row, and into each ranked metric's Validity line in [metrics.md](metrics.md) in the form "AUC x.xx (lo–hi) on n model-panel labels (date), not yet a lead's labels".

| Metric | Repositories | n (meaningful / routine) | AUC (95%) | Size baseline AUC | Alpha | Raters | Date |
| --- | --- | --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — | — | — |

## Limits

- **One lead is one view.** Two labellers who disagree on a third of the changes would be normal for this task (Herzig et al.); validate counts labellers, and a second lead's labels on the same sample show whether the AUC holds across them.
- **The AUC describes the sample.** Within a window it is the window's own mix; across windows, each metric is scored only on its own window's half, so the two halves never mix in one AUC.
- **A change is the grain of the labels, not of every metric.** `reviews_given@1` ranks reviewers by the pull requests they reviewed; its validity check asks whether review goes to the changes that matter, which is the property its ranks reward.
- **What the card cannot hide.** A subject or a patch can name its author; a lead who recognises the code may know who wrote it.
- **Missing GitHub data reads as no review** only where the run has GitHub data for the repository; elsewhere `reviews_given@1` scores nothing. A sync that missed pull requests in a repository it read does turn their reviews into 0: check the sync's completeness before labelling.
- **Rebase merges.** A pull request merged by rebase is one change when its landings share the merge's committer second ([metrics.md](metrics.md#who_knows1-per-path)); the diff `d` shows is its last landing's.
