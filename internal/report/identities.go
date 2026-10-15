package report

// Identities is the answer of `stillhere identities` and of the MCP server's
// identity_candidates tool: suggested .mailmap merges for a stored run's
// people, each with the evidence for it, for a human to review and commit.
// stillhere never applies one: git's mailmap is the only identity store
// (ADR 0004, docs/spec/git-invocation.md#identity-mailmap-and-bots).
//
// #26 adds the candidates and their evidence, and the text, Markdown and
// mailmap renderings. The MCP server (#15) serves the value as it is, so it
// needs no change when #26 lands.
type Identities struct {
	Provenance Provenance `json:"provenance"`
	Caveats    []Caveat   `json:"caveats"`
}

func (*Identities) writeText(*printer)     {} // #26
func (*Identities) writeMarkdown(*printer) {} // #26
