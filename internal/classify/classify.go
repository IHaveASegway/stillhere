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

// Classify returns the verdict on one path under policy p.
func Classify(p *policy.Policy, in Input) domain.PathClass {
	return domain.PathClass{} // #5
}

// Role returns the role path rules and extension rules give path, or "" when
// none applies. It ignores the deny list, size and attributes.
func Role(p *policy.Policy, path string) domain.Role {
	return "" // #5
}
