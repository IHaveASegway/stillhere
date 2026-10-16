package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PseudonymPrefix starts every id that stands in for an email.
const PseudonymPrefix = "p-"

// Pseudonym is the id that stands in for an email where an answer prints
// none: "p-" and the first ten hex digits of the SHA-256 of the canonical
// (lowercased) email. It is stable, across answers, sessions and runs, so a
// reader can join two answers on it and pass it back to name the person,
// over MCP and on the command line alike (PF-7); it is not anonymous:
// anyone who already holds an email can compute its id and confirm it. 40
// bits keep two people of one organisation apart. The MCP server sends it
// in place of every email (internal/mcp, privacy.go).
func Pseudonym(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return PseudonymPrefix + hex.EncodeToString(sum[:5])
}

// IsPseudonym reports whether s has the form of a pseudonym.
func IsPseudonym(s string) bool {
	if len(s) != len(PseudonymPrefix)+10 || !strings.HasPrefix(s, PseudonymPrefix) {
		return false
	}
	return strings.Trim(s[len(PseudonymPrefix):], "0123456789abcdef") == ""
}
