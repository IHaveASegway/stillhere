package policy

// packagedRevisions numbers every packaged policy stillhere has shipped:
// for each hash a run records for it (Policy.SHA256, over default.toml and
// generated_paths.txt), its revision, as default.toml's header and
// docs/spec/classification.md#packaged-policy-revisions list them. A run
// keeps its policy's hash and source but no revision, so a reader finds the
// revision here, for runs scanned by an older stillhere too.
//
// A change to either packaged file changes the hash, and TestPolicy fails
// until the new hash is listed: under a new revision when a rule, a role,
// a bot or an AI address changes; under the current one when only a
// comment does, since comments change nothing a run counts (the rules'
// comments are the reasons 'stillhere paths' prints). That is how a
// change to the packaged policy bumps its version, the rule for a
// configured input (docs/metrics.md), while the schema version stays 1.
//
// The hashes of revisions 1 and 2 are every packaged policy main had
// before revision 3, computed from each commit that changed the two files.
var packagedRevisions = map[string]int{
	// Revision 1, 2026-10-14: the first, with the roles #57 added.
	"36089aba00b049b29ec93fb36f5ee7da2e7a9ad92d7a6641132437e52d802489": 1,
	"c10b4ad0216ca5e6ab68f8766152b244455bbf2a5a8cc25070fb9464dd030596": 1,
	"43c0841713a4edf8171b51889f8a857cc37866a29b53d28c402833a37f5ef448": 1,
	"ff0ae210005f0b2b255cf053d07cf62d541636dd4966fbca158dbebd3fddf8cd": 1,
	// Revision 2, 2026-10-15 (#102): [ai] version 2, GitHub Copilot's
	// co-author addresses, and GitHub's own address among the bots.
	"1a92b21833649dd9af655f7d0a1bdd867b07ecc4c130c9c80d7c334b90e999c8": 2,
	"39f300454d59f2e104446379e1a827948728b146350f6d207d6d7f5f6deb48ba": 2,
	// Revision 3, 2026-10-16: what counts as product code.
	"bdbfe2296b0f4cd39e891b107073c3a466ffe1fc7246da30c80b2aa3dff4df73": 3,
}

// PackagedRevision returns the revision of the packaged policy whose hash
// is sha256, or 0 when it is none this stillhere knows: a policy file, or
// a packaged policy newer than this stillhere.
func PackagedRevision(sha256 string) int { return packagedRevisions[sha256] }
