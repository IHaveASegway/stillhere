// Package collect is the anti-corruption layer between git and stillhere's
// facts (Eric Evans, Domain-Driven Design, ch. 14). It runs git only through
// the domain.GitRunner port and translates git's formats (porcelain,
// numstat, trailers, attributes) into domain types, so no git format leaks
// past it.
//
// It covers discovery and clone health, the classified tree, history and
// blame. Each collector works on one repository at a time, because a
// repository is a bulkhead: its failure is recorded on that repository and
// the run continues.
package collect
