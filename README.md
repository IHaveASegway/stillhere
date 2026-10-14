# stillhere
what dat code do tho

Change analytics for git and GitHub (what landed, what lasted, who knows it) for tech leads and their agents. Every number traces back to the commits behind it.

> stillhere measures specific properties of code changes: how long they lasted, how much they simplified, how much review they got. It does not measure a person's value, and it must not be the basis for performance or pay decisions.

**Status:** design. Nothing to install yet; milestone M0 (the walking skeleton) is next.

- [Product brief](docs/product.md): what it is, who it is for, milestones, open decisions
- [Metric catalog](docs/metrics.md): every metric's definition, blind spots, and how it can be gamed
- [Architecture](docs/architecture.md): contexts, data ownership, storage, security
- [Decision records](docs/adr/)
- [Implementation contracts](docs/spec/): git commands, classification, collectors
- [Test fixture spec](testdata/fixture/SPEC.md)

License: Apache-2.0 ([ADR 0008](docs/adr/0008-apache-2-public.md)). The LICENSE file arrives with M0.
