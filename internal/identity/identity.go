// Package identity answers who a canonical email is: the display name, whether
// it is a bot, whether a change was AI-assisted, and who gets credit for it
// (docs/spec/git-invocation.md#identity-mailmap-and-bots, ADR 0007).
//
// It is part of the functional core. git's mailmap is the only identity
// store: by the time facts reach this package, git has already mapped every
// alias, and this package never parses a mailmap or merges people by name.
package identity

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/policy"
)

// Directory resolves canonical emails within one fact set.
type Directory struct {
	facts  *domain.FactSet
	policy *policy.Policy
}

// New builds the directory for a fact set under a policy's bot and AI
// patterns.
func New(fs *domain.FactSet, p *policy.Policy) *Directory {
	return &Directory{facts: fs, policy: p} // #9
}

// Name returns the display name for a canonical email: the mailmap's name
// when it supplies one, else the name seen most often with that email.
func (d *Directory) Name(email string) string {
	return "" // #9
}

// Bot reports whether the canonical name or email matches a [bots] fragment.
func (d *Directory) Bot(email string) bool {
	return false // #9
}

// AIAssisted reports whether c carries an [ai] pattern in its author or a
// co-author.
func (d *Directory) AIAssisted(c *domain.Commit) bool {
	return false // #9
}

// Credit returns the canonical email credited for c: its author, or the first
// human co-author when an agent authored it.
func (d *Directory) Credit(c *domain.Commit) string {
	return "" // #9
}
