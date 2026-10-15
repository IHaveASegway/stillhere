# ADR 0014: A lead's labels are the gate; a model panel's are reference evidence

Status: Proposed · 2026-10-15 · Deciders: Joseph Peterson

## Context

Every ranked metric (`durable@1`, `simplifying@1`, `reviews_given@1`) is marked beta, and every answer that shows one carries a `validity` caveat, until it passes the check [product.md](../product.md#how-we-will-know-it-works) sets: a lead labels 50 recent changes meaningful or routine, and the metric separates them with an AUC of at least 0.7. Through M2 the check could not be run as designed:

- There was no way to label. `validate` read a CSV a lead had to assemble by hand, from SHAs found with `git log`, with no sampling rule, so two leads would label different, unrepresentative sets, and a lead would see each change's metric values while choosing them.
- `reviews_given@1` could never be scored (#77): `validate` matched labels to landings, and its clusters are pull requests. A labels file that named pull requests the natural way (`#950,meaningful`) was read as comments (KB-19).
- The owner asked for an MVP that anyone can try on their own repositories, and delegated labelling ("figure out labeling"). Someone trying stillhere today has no evidence either way about the ranks, and no lead has labelled.

Labels judge colleagues' work. They are the user's input, kept outside the repository, and a check that rests on them needs a protocol: Herzig, Just and Zeller found a third of the issue-tracker labels they reviewed wrong ("It's Not a Bug, It's a Feature", ICSE 2013).

## Decision

Two layers, with one rule between them.

1. **A labelling feature in the product.** `stillhere label` samples a stored run's changes and asks about each one with single keys, appending each answer to the labels file at once, so it resumes; `--sheet` writes the same sample as a CSV for a spreadsheet, which `validate` reads back as it is ([validation.md](../validation.md)).
   - The sample is drawn from the changes a ranked metric scores at its own grain, people's only, half from each window the metrics read, and in proportion to each repository's small, medium and large changes within a window (stratified sampling). It is a sequence: a larger `--n` extends a smaller one, and the same run, size and seed give the same sample.
   - It is blinded: no metric value, rank or author, and the login GitHub's merge subjects carry is removed.
   - A change is a landing, or a pull request with the landings that put it on the default branch; a labels row names either (a SHA, `#950`, `pr:950`, `owner/name#950`), and `validate` scores each metric at its own grain (`metrics.Changes`, `metrics.Scorer`), which closes #77. It prints each AUC with a 95% bootstrap interval.
2. **A reference validation that stillhere's maintainers run and publish.** On three or four public repositories in different languages, three model raters with the same rubric, blinded to metric values and to each other, label a stratified sample of about 150 changes; the majority label, Krippendorff's alpha and `validate`'s AUCs with their intervals and n are published in [validation.md](../validation.md#reference-results) and in each metric's Validity line as "AUC x.xx (lo–hi) on n model-panel labels (date), not yet a lead's labels".
3. **The rule.** A metric loses "beta" only on a lead's labels. The reference result is shown beside the beta mark, never in place of it, and no AI judgment reaches stillhere's outputs: the panel's labels are inputs to a published number, as a benchmark's are, and the product's non-goal "AI-produced scores or AI-judged changes" stands.

## Alternatives considered

- **Human labels only** (product.md as written). The gold standard, and still the gate. Alone it leaves every user without evidence until some lead spends an hour, and the owner delegated the labelling.
- **Model labels as the gate.** Fast and repeatable. Rejected: model annotation quality varies by task and must be validated against human labels for each task (Pangakis, Wolken and Fasching, "Automated Annotation with Generative AI Requires Validation", 2023), which for this task has not been done; and three sessions of one model family share its biases, so their agreement overstates what independent people would reach. Making it the gate would also put an AI judgment in the path that decides how a person's rank is presented, which ADR 0006 keeps AI out of.
- **Weak supervision from conventional-commit prefixes and pull request labels** (Snorkel-style labelling functions: Ratner et al., "Snorkel", VLDB 2018). Cheap and large. Rejected as the reference: the labels are the authors' own claims about their changes (a `feat:` prefix is what its author wanted it called), noisy, and absent in many repositories. Kept as a possible cross-check of the panel.
- **A labelling tool outside stillhere** (Label Studio; Explosion's Prodigy, whose one-decision-at-a-time, keyboard-driven design `label` follows). Either can label a CSV, but neither knows which changes the metrics score, the strata, or how to show a change without its metric values and author; a lead would export, label and import by hand. The sheet keeps that path open for anyone who prefers a spreadsheet or a tool.

## Consequences

- Anyone can run the check on their own repositories in about half an hour, and the result is reproducible: the sample is fixed by run, size and seed, and the labels file is plain CSV.
- `validate` and `label` read one labels format; a row whose first field is `#` and digits is a pull request, every other `#` line a comment. `skip` and an empty label are no label.
- A published reference AUC will be quoted. The Validity line's wording ("not yet a lead's labels") and its interval and n are there so that it is quoted with its weight.
- One more direct dependency in `internal/cli`: `golang.org/x/term`, already in the module graph through go-gh, for single-key input; no new module in `go.sum`.
- The panel itself, its prompts and its aggregation script, lives outside the product: they are how a number in the documentation was made, not something stillhere runs.

## Door

Two-way for the tooling: `label` and the labels format are a command and a file a release can change. The rule that only a lead's labels lift "beta" is a product principle: changing it later would change what every Validity line and caveat means, so it is recorded here.

## Revisit if

- A lead's labels and the panel's majority disagree on most changes of the same sample (alpha between them below 0.667): the panel is not a usable reference for this task, and the reference result should come down.
- The panel's AUCs and a lead's AUCs agree within their intervals across several organizations: the reference could carry more weight, for example as a provisional bar.
- Leads label with `--sheet` far more than interactively: the card's design matters less than the sheet's columns.
