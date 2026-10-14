# ADR 0009: Rank intervals: a bootstrap per metric kind, reproducible by derived seeds

Status: Proposed · 2026-10-14 · Deciders: Joseph Peterson · Amends the interval method of [ADR 0001](0001-measure-changes-rank-per-metric.md)

## Context

[ADR 0001](0001-measure-changes-rank-per-metric.md) decided that every rank shows a 95% interval from a seeded bootstrap, and named one method: a cluster bootstrap over the person's landings. That fits a ratio such as `durable@1`, but not a sum such as `simplifying@1` or `reviews_given@1`. A total depends on how many changes a person landed, and resampling the person's own changes holds that number fixed: for a pure count, every replicate returns the observed count. Ten weekly changes get the interval [10, 10] that way, and [7, 13] when the window's weeks are resampled.

The method must also be:

- **honest at the sizes ranks start at**: 10 landings, or 10 reviewed pull requests;
- **reproducible**: intervals are stored with the run, and recomputing them from its facts must give the same numbers ([run model](../spec/codebase.md#run-model));
- **in the functional core**, which imports the standard library only ([dependency rule](../spec/codebase.md#the-dependency-rule)).

## Decision

- **Ratio metrics** (`durable@1`) resample each person's clusters, their landings, with replacement: a cluster bootstrap (Efron and Tibshirani, *An Introduction to the Bootstrap*, 1993). Each person draws from their own random stream, so a colleague joining the scope never moves their value interval.
- **Sum metrics** (`simplifying@1`, `reviews_given@1`) resample the window's 7-day blocks, counted back from its end, the same blocks for every person: a block bootstrap (Künsch, Ann. Statist., 1989) on fixed calendar weeks. The metric hands over each change's block and the number of blocks, and `rank.Intervals` panics when either disagrees with the window, because a wrong count gives plausible but wrong intervals.
- **Intervals** run from the 25th to the 975th of B = 1,000 sorted replicates (the percentile interval), for values and for ranks. Ties take the best rank they cover ("1, 1, 3"). People below the metric's minimum are listed, not ranked, and get no interval.
- **Seeds**: SHA-256 over `metric@version`, scope and window. Each stream is SHA-256 over the seed and its label, a splittable generator (Claessen and Pałka, Haskell Symposium 2013), feeding PCG-DXSM through stillhere's own bounded draw (Lemire, ACM TOMACS, 2019). A golden-master test pins the seed, the stream and the draw.
- **Changing any of this moves published numbers**, so it is done like a metric version bump: a changelog entry and a new golden master.

## Alternatives considered

- **Resampling a sum's own clusters.** A count gets a zero-width interval, as above.
- **Independent week blocks per person.** Hall and Miller ("Using the Bootstrap to Quantify the Authority of an Empirical Ranking", Ann. Statist., 2009) warn that resampling the same units for every ranked item can mislead when the items far outnumber the units. In the simulation below, at 13 weeks and with no week that affects the whole team, the two are indistinguishable: they cover the true rank 93.6% and 94.1% of the time at 30 people, with the same widths. When a week does affect everyone, such as a holiday, independent blocks treat it as each person's own noise: 97.7% coverage against 93.2%, from intervals 29% wider (14.1 ranks of 30 against 10.9). Shared blocks keep what a week did to everyone out of the comparison.
- **The delta method for ratios** (Deng, Knoblich and Lu, KDD 2018). Closed-form and cheaper, but it gives value intervals only, rank intervals need simulation anyway, and its normal approximation is poor for a ratio piled up near 1.0.
- **BCa or bootstrap-t intervals** (Efron and Tibshirani, ch. 14 and 12). Better coverage at small samples (see Consequences), for a jackknife per person and a harder explanation. Deferred, not rejected.
- **`math/rand/v2`'s `IntN`, or one stream for everyone.** `IntN`'s algorithm is not part of its documented API, and with one stream every interval moves when anyone joins.
- **A seed without the window.** A ratio interval would then stay fixed while a person's landings do not change; a sum's would still move, because its week blocks move with now. Kept as the first remedy if moving ends confuse people.

## Consequences

- **Monte Carlo error.** An end is one of 1,000 draws, with an error of about half a percentage point of probability, and a new window reseeds. So a later scan can move an end with no new data, and two ratio subjects with equal data can get different ends; two sum subjects with equal weekly totals get equal intervals. On the sibling fixture, Lena's `durable@1` interval starts at 20/30 for 62% of seeds and at 19/30 for 37%.
- **Coverage below 95% at the sizes ranks start at.** A simulation ran `rank`'s own bootstrap over 150 to 400 simulated teams per setting. For sums, each person landed a Poisson number of changes a week at a lognormal rate (σ = 0.6, or 0.2 for near-equals), with and without a team-wide weekly multiplier (Gamma, mean 1, coefficient of variation 0.71). For ratios, each landing kept its lines at a rate drawn around the person's durability (Beta, φ = 4). Its code is in the review notes of pull request #32.

  | Setting | Value intervals cover | Rank intervals cover |
  | --- | --- | --- |
  | ratio, 10 people, 10 landings each | 86.9% | 95.4% |
  | ratio, 20 and 40 landings | 90.3%, 91.1% | 96.5%, 97.3% |
  | sum over 13 weeks, 10 or 30 people | 88–91% | 91–97% |
  | sum over 13 weeks, 30 near-equal people | 91.1% | 88.3% |
  | ratio at 200 landings; sum over 104 weeks | 94.4%; 94.1% | 99.0%; 98.0% |

  The last row shows the method converging as it should: the shortfall is the percentile interval's at small samples.
- **Zero-width value intervals** for a person whose landings all have the same ratio, common for `durable@1` at 100%: a bootstrap shows only the variation its sample holds.
- **Marginal intervals.** Each is about one person. If leads read the table as a whole, simultaneous sets exist (Mogstad, Romano, Shaikh and Wilhelm, "Inference for Ranks", Rev. Econ. Stud., 2024).
- **Cost**: 500 people with 100 clusters each take about 250 ms for a ratio metric and 65 ms for a sum (Apple silicon, best of five).

## Door

Two-way technically: intervals are recomputable from stored facts. One-way socially, as ADR 0001 says of ranks: once leads have seen intervals, a new method, seed scheme or B moves every published number.

## Revisit if

- A labelled sample, or a simulation shaped like a real team, confirms value intervals covering under 90% at the eligibility floor: move to BCa or bootstrap-t intervals, or raise the minimum, which [#24](https://github.com/IHaveASegway/stillhere/issues/24) decides.
- People report ends moving between scans with no new landings: derive the seed without the window, or raise B (at B = 10,000 the Monte Carlo error is 0.16 points, for ten times the cost).
- A scope ranks far more people than its window has weeks, or a simulation shaped like a real team stops favouring shared blocks: switch sums to independent blocks.
