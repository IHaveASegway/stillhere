package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// This file holds the bytes a run stores for its policy (Policy.Source,
// domain.Run.PolicySource) and the hash of them (Policy.SHA256).
//
// Source is the TOML and the deny list text framed by a length prefix:
//
//	<byte length of the TOML, in decimal> LF <the TOML> <the deny list text>
//
// The packaged policy's deny list text is generated_paths.txt; a user file
// keeps its deny list inside the TOML, so its text is empty. The prefix makes
// the framing injective: concatenating the two parts without it would let an
// edit move bytes from the end of the TOML to the start of the deny list and
// keep the hash, the canonicalisation problem that length-prefixed encodings
// such as netstrings exist to prevent. A reader can check a run's hash of a
// user file by hand:
//
//	{ wc -c < stillhere.toml | tr -d ' '; cat stillhere.toml; } | shasum -a 256
//
// A later framing would start with a byte that is not a digit, so FromSource
// could tell the two apart.

// maxPrefixDigits bounds the length prefix: 19 digits hold any int64.
const maxPrefixDigits = 19

// newSource frames tomlText and denyText as Policy.Source. The result shares
// no memory with either argument.
func newSource(tomlText, denyText []byte) []byte {
	prefix := strconv.Itoa(len(tomlText))
	b := make([]byte, 0, len(prefix)+1+len(tomlText)+len(denyText))
	b = append(b, prefix...)
	b = append(b, '\n')
	b = append(b, tomlText...)
	return append(b, denyText...)
}

// openSource splits a Policy.Source back into the TOML and the deny list
// text. It accepts only what newSource writes, so a source opens one way
// only: the prefix is digits, without a sign, and without a leading zero
// unless it is 0.
func openSource(source []byte) (tomlText, denyText []byte, err error) {
	prefix, rest, ok := bytes.Cut(source, []byte{'\n'})
	switch {
	case !ok:
		return nil, nil, errors.New("no length prefix")
	case len(prefix) == 0 || len(prefix) > maxPrefixDigits:
		return nil, nil, fmt.Errorf("length prefix %q is not a byte count", prefix)
	case len(prefix) > 1 && prefix[0] == '0':
		return nil, nil, fmt.Errorf("length prefix %q has a leading zero", prefix)
	}
	for _, c := range prefix {
		if c < '0' || c > '9' {
			return nil, nil, fmt.Errorf("length prefix %q is not a byte count", prefix)
		}
	}
	n, err := strconv.Atoi(string(prefix))
	if err != nil {
		return nil, nil, fmt.Errorf("length prefix %q: %w", prefix, err)
	}
	if n > len(rest) {
		return nil, nil, fmt.Errorf("length prefix %d exceeds the %d bytes that follow it", n, len(rest))
	}
	return rest[:n], rest[n:], nil
}

// sourceHash is the hex SHA-256 of source: Policy.SHA256.
func sourceHash(source []byte) string {
	sum := sha256.Sum256(source)
	return hex.EncodeToString(sum[:])
}
