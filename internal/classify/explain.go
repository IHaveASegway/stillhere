package classify

import (
	"fmt"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// RuleKind is the step of the classifier that decided a path's class
// (docs/spec/classification.md#order).
type RuleKind string

// The steps, in the classifier's order.
const (
	RuleDeny      RuleKind = "deny"      // a pattern of the policy's deny list
	RuleAttribute RuleKind = "attribute" // linguist-generated or linguist-vendored set in .gitattributes, with no deny pattern
	RuleSize      RuleKind = "size"      // [blame] max_bytes, or a path rule's max_bytes
	RuleHeader    RuleKind = "header"    // a generated-file header, which the header sniff found
	RuleNotFile   RuleKind = "not-file"  // a symbolic link or a submodule, which gets no role
	RulePath      RuleKind = "path"      // a [[roles.path]] rule
	RuleName      RuleKind = "name"      // a [roles.names] entry
	RuleExtension RuleKind = "extension" // a [roles.extensions] entry
	RuleNone      RuleKind = "none"      // no name, extension or path rule gives the path a role
	RuleUnknown   RuleKind = "unknown"   // this classifier gives the path another class under the run's policy: an older stillhere classified it
)

// The sources a Rule names: where the rule that decided lives.
const (
	SourcePackaged  = "packaged policy"    // the policy stillhere ships, with its deny list
	SourceFile      = "policy file"        // a policy file: --policy FILE or ./stillhere.toml
	SourceAttribute = "linguist attribute" // the repository's .gitattributes at the scanned commit
	SourceHeader    = "generated header"   // the file's own first 40 lines
	SourceFileType  = "file type"          // the tree entry's mode
)

// Rule is the rule that decided a path's class, as Explain finds it: what
// 'stillhere paths' prints beside each path.
type Rule struct {
	Kind RuleKind
	// Text is the rule as the policy writes it: a pattern, an extension
	// (".go"), a name ("Dockerfile"), a bound ("max_bytes = 400000"); for
	// a step outside the policy, what it found.
	Text string
	// Where locates it in the policy: "generated_paths.txt line 436",
	// "[paths] deny pattern 3", "[[roles.path]] rule 5", "[roles.extensions]
	// prod", "[roles.names] iac", "[blame] max_bytes"; ".gitattributes" for
	// an attribute; "" for a step outside the policy.
	Where string
	// Source is where the rule lives: SourcePackaged or SourceFile for the
	// policy's rules, else SourceAttribute, SourceHeader or SourceFileType.
	Source string
	// Note says why, when anything does: the rule's own comment in the
	// policy file, the deny list's label for its pattern, the path's size,
	// a deny pattern .gitattributes overrode.
	Note string
}

// Explain says which rule decided c, a class that Classify gave under p:
// the step that decided and, for the policy's own rules, which rule and
// where the policy holds it. It reads the same rules Classify does, through
// the same functions (decide, Paths.Denying), so a stored run's classes are
// explained from the run's policy alone, with no git, whenever they were
// stored: the inputs that are not the path, its size and the policy (the
// attributes, the header sniff, whether HEAD holds a file) are told apart
// by what the class says. What the class cannot tell, which of the two
// linguist attributes denied a path or un-denied it, is said as either.
//
// A class this classifier would not give under p, as a stillhere older
// than a change to the classifier may have stored, is RuleUnknown: the
// rule that decided it is not known, and a new scan names it.
func Explain(p *policy.Policy, c domain.PathClass) Rule {
	source := SourceFile
	if p.Packaged() {
		source = SourcePackaged
	}
	unknown := Rule{Kind: RuleUnknown, Note: "under the run's policy this stillhere's classifier gives the path another class: " +
		"an older stillhere classified it, and a new scan names its rule"}
	switch c.Skip {
	case domain.SkipDeniedPath:
		if d, ok := p.Paths.Denying(c.Path); ok {
			return Rule{Kind: RuleDeny, Text: d.Pattern, Where: denyWhere(p, d), Source: source, Note: d.Note}
		}
		return Rule{Kind: RuleAttribute, Text: "linguist-generated or linguist-vendored", Where: ".gitattributes", Source: SourceAttribute,
			Note: "no deny pattern matches the path: an attribute at the scanned commit marks it generated or vendored"}
	case domain.SkipTooBig:
		if c.Size > p.Blame.MaxBytes {
			return Rule{Kind: RuleSize, Text: fmt.Sprintf("max_bytes = %d", p.Blame.MaxBytes), Where: "[blame] max_bytes",
				Source: source, Note: sizeNote(c.Size)}
		}
		if d := decide(p, c.Path, c.Size); d.tooBig {
			r := p.Roles.Path[d.rule]
			return Rule{Kind: RuleSize, Text: fmt.Sprintf("max_bytes = %d", r.MaxBytes), Where: fmt.Sprintf("[[roles.path]] rule %d", d.rule+1),
				Source: source, Note: joinNotes(sizeNote(c.Size), r.Note)}
		}
		return unknown
	case domain.SkipGeneratedHeader:
		return Rule{Kind: RuleHeader, Text: "a generated-file header in the first 40 lines", Source: SourceHeader,
			Note: "such as \"Code generated ... DO NOT EDIT\"; turning linguist-generated off for the path in .gitattributes counts it"}
	case domain.SkipUnclassified:
		d := decide(p, c.Path, c.Size)
		switch {
		case d.tooBig:
			return unknown
		case d.role != "":
			return Rule{Kind: RuleNotFile, Text: "a symbolic link or a submodule", Source: SourceFileType,
				Note: "only a regular file gets a role"}
		}
		return Rule{Kind: RuleNone, Text: "no name, extension or path rule", Source: source,
			Note: "a role for it in [roles.extensions], [roles.names] or [[roles.path]] makes it count"}
	case "":
	default:
		return unknown
	}
	d := decide(p, c.Path, c.Size)
	if d.tooBig || d.role != c.Role {
		return unknown
	}
	var r Rule
	switch d.by {
	case RulePath:
		pr := p.Roles.Path[d.rule]
		r = Rule{Kind: RulePath, Text: pr.Pattern.String(), Where: fmt.Sprintf("[[roles.path]] rule %d", d.rule+1), Note: pr.Note}
	case RuleName:
		r = Rule{Kind: RuleName, Text: d.key, Where: "[roles.names] " + string(d.role)}
	default:
		r = Rule{Kind: RuleExtension, Text: d.key, Where: "[roles.extensions] " + string(d.role)}
	}
	r.Source = source
	if dr, ok := p.Paths.Denying(c.Path); ok {
		r.Note = joinNotes(r.Note, "deny pattern "+dr.Pattern+" matches it, but .gitattributes turns linguist-generated or linguist-vendored off")
	}
	return r
}

// denyWhere locates a deny pattern: its line in the packaged list, or its
// place in a policy file's [paths] deny.
func denyWhere(p *policy.Policy, d policy.DenyRule) string {
	if file, ok := strings.CutPrefix(p.Paths.Source, "package:"); ok && d.Line > 0 {
		return fmt.Sprintf("%s line %d", file, d.Line)
	}
	return fmt.Sprintf("[paths] deny pattern %d", d.Index)
}

// sizeNote is a path's size as a Rule's note gives it.
func sizeNote(size int64) string { return fmt.Sprintf("%d bytes", size) }

// joinNotes joins the non-empty notes with "; ".
func joinNotes(notes ...string) string {
	var out []string
	for _, n := range notes {
		if n != "" {
			out = append(out, n)
		}
	}
	return strings.Join(out, "; ")
}
