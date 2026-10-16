package policy

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// This file writes a policy back out as a policy file: what
// 'stillhere policy' prints, so that 'stillhere policy > stillhere.toml'
// starts a policy of one's own from the one a scan would use.

// File returns the policy as a complete policy file, which Parse reads back
// to the same rules. A user's file is returned as it was read. The packaged
// policy is its TOML with its header comment replaced by one that says
// where the file came from, and [paths] package_file replaced by
// deny = [...]: every pattern of the packaged deny list, in order, with the
// list's comments, among them the license notice of the patterns it
// translates from GitHub Linguist (MIT), which travels with them. A user
// file's hash covers the patterns written into it, as it covers every byte
// of the file, while package_file names a file inside stillhere that no
// user file may name (classification.md#deny-list).
//
// It reads Source alone, so a stored run's policy (FromSource) is written
// out the same way, whatever today's packaged files say.
func (p *Policy) File() ([]byte, error) {
	tomlText, denyText, err := openSource(p.Source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name, err)
	}
	if !p.Packaged() {
		return slices.Clone(tomlText), nil
	}
	lines := strings.SplitAfter(string(tomlText), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, fileHeader, p.SHA256)
	start := 0
	for start < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[start]), "#") {
		start++ // the packaged policy's own header, which describes the packaged file
	}
	table, replaced := "", false
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			table = trimmed
		}
		if table == "[paths]" && !replaced && fileKey(trimmed) == "package_file" {
			b.WriteString(denyArray(string(denyText)))
			replaced = true
			continue
		}
		b.WriteString(line)
	}
	if !replaced {
		return nil, fmt.Errorf("%s: no [paths] package_file line to write the deny list in place of", p.Name)
	}
	return []byte(b.String()), nil
}

// fileHeader is the header comment of a packaged policy written out as a
// file; the verb is the packaged policy's hash.
const fileHeader = `# A stillhere policy: what a run counts, and how
# (docs/spec/classification.md). 'stillhere policy' printed it from the
# packaged policy whose hash is
#   sha256 %s
# with [paths] deny written out in full from generated_paths.txt.
#
# Edit it, then keep it as ./stillhere.toml or pass it with --policy FILE.
# It replaces the packaged policy as a whole: every table and every key
# below stays required, and nothing is filled in from the packaged policy,
# so the policy hash a run records describes all of it.
`

// fileKey returns the key a TOML line assigns, or "" for a line that is
// not a key = value pair.
func fileKey(line string) string {
	key, _, ok := strings.Cut(line, "=")
	if !ok || strings.HasPrefix(line, "#") {
		return ""
	}
	return strings.TrimSpace(key)
}

// denyArray writes a deny list's text, in generated_paths.txt's format, as
// a TOML array: each pattern one element, each comment line a comment, and
// the blank lines kept, so the file reads as the list does.
func denyArray(text string) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	b.WriteString("deny = [\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			b.WriteString("\n")
		case strings.HasPrefix(trimmed, "#"):
			b.WriteString("  " + strings.TrimRightFunc(line, unicode.IsSpace) + "\n")
		default:
			b.WriteString("  " + tomlString(trimmed) + ",\n")
		}
	}
	b.WriteString("]\n")
	return b.String()
}

// tomlString quotes s as a TOML string: a literal string, which keeps a
// pattern's backslashes as they are, unless s holds a single quote or a
// control character other than a tab, which only a basic string can hold.
func tomlString(s string) string {
	literal := !strings.ContainsFunc(s, func(r rune) bool {
		return r == '\'' || unicode.IsControl(r) && r != '\t'
	})
	if literal {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
