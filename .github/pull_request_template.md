## Summary

<!-- What changes for a user or a contributor, in a few sentences. -->

Closes #

## Decisions

<!--
Each decision this change makes: the choice, the main alternative, and why
this one. Name the pattern when one applies (outbox, bulkhead, anti-corruption
layer, ...) with one canonical source. A decision that is hard to reverse
gets an ADR in docs/adr/.
-->

## Contract changes

<!--
Signatures in docs/spec/codebase.md, internal/domain, policy/types.go,
published formats (report JSON, metric ids and versions), the facts.db
schema. Write "None" when there are none.
-->

## Verification

<!-- The commands you ran and the output that matters. -->

## Follow-ups

<!-- What this leaves for later, with issue numbers. -->

---

- [ ] The title is a Conventional Commit (`feat:`, `fix:`, `docs:`, `test:`, `ci:`, `chore:`, `refactor:`); the changelog groups by it.
- [ ] `make lint` and `make test` pass. New tests are the ones docs/spec/codebase.md#tests names, or the PR says why.
- [ ] No fixture oracle moved, or its new value was derived by hand in testdata/fixture/SPEC.md first.
- [ ] No real person's name, email or data, and no token, in code, tests, fixtures or this description.
