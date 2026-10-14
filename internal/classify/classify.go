// Package classify is the ordered path classifier: denied path, too big,
// generated header, then role, else unclassified
// (docs/spec/classification.md#order). The week and blame collectors both
// call it, so there is no second, looser filter.
//
// It is part of the functional core: Classify decides from facts that
// internal/collect has already gathered with git (size, attributes, header
// sniff), and never runs anything itself.
package classify

import (
	"slices"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// Input is everything the classifier needs to know about one path.
type Input struct {
	Path      string
	AtHead    bool   // the path exists at Head; false for a path only the history touched
	Size      int64  // bytes at Head, else at the commit that touched the path; -1 when unknown: no too_big check
	Generated string // linguist-generated at Head: "unspecified", "unset", "set" or a value
	Vendored  string // linguist-vendored at Head
	HeaderHit bool   // header sniff matched within the first 40 lines; always false when AtHead is false, because the sniff reads Head
}

// Classify returns the verdict on one path under policy p, by the first
// rule that applies:
//
//  1. denied_path: a deny pattern matches, unless .gitattributes turns
//     linguist-generated or linguist-vendored off; either attribute turned
//     on denies the path whatever the patterns say, and on wins over off;
//  2. too_big: the size is known and above [blame] max_bytes;
//  3. generated_header: the header sniff hit, unless linguist-generated is
//     off, because an explicit attribute settles whether a file is
//     generated, as it does for GitHub Linguist;
//  4. the role that Role gives;
//  5. unclassified.
//
// An attribute is on when git check-attr reports set or any value but
// false, and off when it reports unset or the value false: Linguist reads
// linguist-generated=false like -linguist-generated.
func Classify(p *policy.Policy, in Input) domain.PathClass {
	c := domain.PathClass{Path: in.Path, Size: in.Size, AtHead: in.AtHead}
	generated, vendored := attribute(in.Generated), attribute(in.Vendored)
	switch {
	case denied(p, in.Path, generated, vendored):
		c.Skip = domain.SkipDeniedPath
	case in.Size >= 0 && in.Size > p.Blame.MaxBytes:
		c.Skip = domain.SkipTooBig
	case in.AtHead && in.HeaderHit && generated != attrOff:
		c.Skip = domain.SkipGeneratedHeader
	default:
		c.Role = Role(p, in.Path)
		if c.Role == "" {
			c.Skip = domain.SkipUnclassified
		}
	}
	return c
}

// Role returns the role path rules and extension rules give path, or "" when
// none applies. It ignores the deny list, size and attributes.
//
// The file name's role comes first: an exact [roles] names entry, else the
// lowercased last extension. Then the path rules are tried in order, and the
// first whose pattern matches path and whose When admits the name's role
// wins; a rule without When admits any path. No rule matching leaves the
// name's role.
func Role(p *policy.Policy, path string) domain.Role {
	base := nameRole(p, path)
	for _, r := range p.Roles.Path {
		if r.Pattern.MatchString(path) && (len(r.When) == 0 || base != "" && slices.Contains(r.When, base)) {
			return r.Role
		}
	}
	return base
}

// nameRole is the role the file name gives: an exact name, else the
// extension, its last dot suffix as path.Ext finds it (".gz" in
// "x.tar.gz", ".bashrc" in ".bashrc").
func nameRole(p *policy.Policy, file string) domain.Role {
	name := file[strings.LastIndexByte(file, '/')+1:]
	if r, ok := p.Roles.Names[name]; ok {
		return r
	}
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return ""
	}
	return p.Roles.Extensions[strings.ToLower(name[dot:])]
}

// attrState is how .gitattributes sets one linguist attribute on a path.
type attrState int

const (
	attrUnspecified attrState = iota
	attrOn
	attrOff
)

// attribute reads one check-attr info value the way GitHub Linguist reads
// a boolean attribute (lazy_blob.rb, boolean_attribute): unset and the
// value false are off; set and every other value are on. An empty info, as
// in an Input built without attributes, is unspecified.
func attribute(info string) attrState {
	switch info {
	case "", "unspecified":
		return attrUnspecified
	case "unset", "false":
		return attrOff
	default:
		return attrOn
	}
}

// denied decides the denied_path step (docs/spec/classification.md#gitattributes):
// the deny patterns, then an attribute turned off un-denies, then an
// attribute turned on denies. Reading the attributes first gives the same
// answer and skips the patterns when an attribute settles it.
func denied(p *policy.Policy, path string, generated, vendored attrState) bool {
	switch {
	case generated == attrOn || vendored == attrOn:
		return true
	case generated == attrOff || vendored == attrOff:
		return false
	}
	return p.Paths.Denies(path)
}
