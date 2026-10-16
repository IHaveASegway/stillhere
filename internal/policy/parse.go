package policy

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// schemaVersion is the only policy schema version this stillhere reads.
const schemaVersion = 1

// MaxWeekDays bounds [week] days, and --days with it: a century, far
// beyond any history, and small enough that no day arithmetic on the window
// can overflow.
const MaxWeekDays = 36500

// The values the schema allows for [headline] metric and [week] sort.
const (
	headlineProdNonblank = "prod_nonblank_lines"
	sortLinesAdded       = "lines_added"
	sortCommits          = "commits"
)

// The values of Paths.Source.
const (
	pathsFromPackage = "package:" // + the package file's name
	pathsFromPolicy  = "policy"
)

// origin is where a policy's bytes came from, which decides how [paths]
// may name its deny list.
type origin int

const (
	// fromUser is a user's file (Parse): [paths] deny only, because the hash
	// of a user's file could not cover a file packaged inside stillhere.
	fromUser origin = iota
	// fromPackage is the embedded default (Default): [paths] package_file
	// only, naming the embedded deny list.
	fromPackage
	// fromStore is a run's stored source (FromSource): whichever of the two
	// the policy had, with a package file's text taken from the source.
	fromStore
)

// file is the TOML schema of a policy. Field types are what the TOML must
// hold; the checks in build add what types cannot say.
type file struct {
	Version  int          `toml:"version"`
	Headline headlineFile `toml:"headline"`
	Week     weekFile     `toml:"week"`
	Blame    blameFile    `toml:"blame"`
	Bots     botsFile     `toml:"bots"`
	AI       aiFile       `toml:"ai"`
	Roles    rolesFile    `toml:"roles"`
	Paths    pathsFile    `toml:"paths"`
}

type headlineFile struct {
	Metric string `toml:"metric"`
}

type weekFile struct {
	Days int    `toml:"days"`
	Sort string `toml:"sort"`
}

type blameFile struct {
	MaxBytes int64 `toml:"max_bytes"`
}

type botsFile struct {
	Fragments []string `toml:"fragments"`
}

type aiFile struct {
	Version        int      `toml:"version"`
	CoauthorEmails []string `toml:"coauthor_emails"`
	AgentAuthors   []string `toml:"agent_authors"`
}

type rolesFile struct {
	Extensions map[string][]string `toml:"extensions"` // role → extensions
	Names      map[string][]string `toml:"names"`      // role → file names
	Path       []pathRuleFile      `toml:"path"`
}

type pathRuleFile struct {
	Role     string   `toml:"role"`
	Pattern  string   `toml:"pattern"`
	When     []string `toml:"when"`
	MaxBytes *int64   `toml:"max_bytes"` // optional, as when is; nil when absent
}

type pathsFile struct {
	Deny        []string `toml:"deny"`
	PackageFile string   `toml:"package_file"`
}

// known reports whether the schema has key k, a TOML key path. A key of a
// role table ([roles.extensions], [roles.names]) is a role name. Keys match
// exactly: the decoder would also fill a field from a key that differs only
// in case, and with both "days" and "Days" in a file it would keep whichever
// its map iteration reached last.
func known(k toml.Key) bool {
	switch len(k) {
	case 1:
		switch k[0] {
		case "version", "headline", "week", "blame", "bots", "ai", "roles", "paths":
			return true
		}
	case 2:
		switch k[0] {
		case "headline":
			return k[1] == "metric"
		case "week":
			return k[1] == "days" || k[1] == "sort"
		case "blame":
			return k[1] == "max_bytes"
		case "bots":
			return k[1] == "fragments"
		case "ai":
			return k[1] == "version" || k[1] == "coauthor_emails" || k[1] == "agent_authors"
		case "roles":
			return k[1] == "extensions" || k[1] == "names" || k[1] == "path"
		case "paths":
			return k[1] == "deny" || k[1] == "package_file"
		}
	case 3:
		switch {
		case k[0] == "roles" && (k[1] == "extensions" || k[1] == "names"):
			return isRole(k[2])
		case k[0] == "roles" && k[1] == "path":
			return k[2] == "role" || k[2] == "pattern" || k[2] == "when" || k[2] == "max_bytes"
		}
	}
	return false
}

// required lists, in the order messages report them, what every policy must
// define: each required table (docs/spec/report-json.md#exit-codes) and
// every key in it. [paths] holds one of two keys, so build checks it apart.
func required() [][]string {
	return [][]string{
		{"version"},
		{"headline"}, {"headline", "metric"},
		{"week"}, {"week", "days"}, {"week", "sort"},
		{"blame"}, {"blame", "max_bytes"},
		{"bots"}, {"bots", "fragments"},
		{"ai"}, {"ai", "version"}, {"ai", "coauthor_emails"}, {"ai", "agent_authors"},
		{"roles"}, {"roles", "extensions"}, {"roles", "names"}, {"roles", "path"},
		{"paths"},
	}
}

// build decodes and checks a policy's TOML and, for a package file, its deny
// list text, and returns the policy or every problem found, in a stable
// order: unknown keys in file order, then missing tables and keys, then bad
// values, in schema order.
func build(name string, tomlText, denyText []byte, from origin) (*Policy, []string) {
	var f file
	md, err := toml.Decode(string(tomlText), &f)
	if err != nil {
		return nil, []string{strings.TrimPrefix(err.Error(), "toml: ")}
	}
	c := &checker{md: md}
	c.keys()
	for _, k := range required() {
		c.require(k...)
	}
	if len(c.problems) > 0 {
		// Both lists at once: a misspelt key shows up as unknown and as the
		// key it was meant to be. The values are not checked, because a
		// missing one reads as zero.
		return nil, c.problems
	}

	p := &Policy{
		Name:     name,
		Version:  f.Version,
		Headline: Headline{Metric: f.Headline.Metric},
		Week:     Week{Days: f.Week.Days, Sort: f.Week.Sort},
		Blame:    Blame{MaxBytes: f.Blame.MaxBytes},
		Bots:     Bots{Fragments: c.fragments("[bots] fragments", f.Bots.Fragments)},
		AI: AI{
			Version:        f.AI.Version,
			CoauthorEmails: c.emails("[ai] coauthor_emails", f.AI.CoauthorEmails),
			AgentAuthors:   c.fragments("[ai] agent_authors", f.AI.AgentAuthors),
		},
	}
	if f.Version != schemaVersion {
		c.problemf("version = %d: this stillhere reads policy version %d", f.Version, schemaVersion)
	}
	if f.Headline.Metric != headlineProdNonblank {
		c.problemf("[headline] metric %q: the only headline is %q", f.Headline.Metric, headlineProdNonblank)
	}
	if f.Week.Days < 1 || f.Week.Days > MaxWeekDays {
		c.problemf("[week] days = %d: want 1 to %d", f.Week.Days, MaxWeekDays)
	}
	if f.Week.Sort != sortLinesAdded && f.Week.Sort != sortCommits {
		c.problemf("[week] sort %q: want %q or %q", f.Week.Sort, sortLinesAdded, sortCommits)
	}
	if f.Blame.MaxBytes < 1 {
		c.problemf("[blame] max_bytes = %d: want 1 or more", f.Blame.MaxBytes)
	}
	if f.AI.Version < 1 {
		c.problemf("[ai] version = %d: want 1 or more", f.AI.Version)
	}
	p.Roles = c.roles(f.Roles)
	if notes := ruleNotes(string(tomlText)); len(notes) == len(p.Roles.Path) {
		for i := range p.Roles.Path {
			p.Roles.Path[i].Note = notes[i]
		}
	}
	p.Paths = c.paths(f.Paths, denyText, from)
	if len(c.problems) > 0 {
		return nil, c.problems
	}
	p.Source = newSource(tomlText, denyText)
	p.SHA256 = sourceHash(p.Source)
	return p, nil
}

// checker collects the problems of one policy.
type checker struct {
	md       toml.MetaData
	problems []string
}

func (c *checker) problemf(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

// keys reports every key the schema does not know, once each, in file
// order. The keys inside an unknown table are not reported again.
func (c *checker) keys() {
	var unknown []toml.Key
	for _, k := range c.md.Keys() {
		if known(k) || slices.ContainsFunc(unknown, func(u toml.Key) bool { return hasPrefix(k, u) }) {
			continue
		}
		unknown = append(unknown, k)
		switch {
		case len(k) == 3 && k[0] == "roles" && (k[1] == "extensions" || k[1] == "names"):
			c.problemf("[roles.%s] %s: not a role (want one of %s)", k[1], quoteKey(k[2]), roleList())
		case c.md.Type(k...) == "Hash":
			c.problemf("unknown table [%s]", dotted(k))
		case c.md.Type(k...) == "ArrayHash":
			c.problemf("unknown table [[%s]]", dotted(k))
		default:
			c.problemf("unknown key %s", keyName(k))
		}
	}
}

// hasPrefix reports whether key path k starts with prefix.
func hasPrefix(k, prefix toml.Key) bool {
	return len(k) >= len(prefix) && slices.Equal(k[:len(prefix)], prefix)
}

// require reports a missing table or key. A key of a missing table is not
// reported again.
func (c *checker) require(key ...string) {
	if len(key) > 1 && !c.md.IsDefined(key[:len(key)-1]...) {
		return
	}
	if c.md.IsDefined(key...) {
		return
	}
	switch {
	case len(key) == 1 && key[0] == "version":
		c.problemf("missing key version")
	case len(key) == 1:
		c.problemf("missing table [%s]", key[0])
	default:
		c.problemf("missing key %s", keyName(key))
	}
}

// fragments lowercases identity fragments. An empty fragment would match
// every identity.
func (c *checker) fragments(field string, in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) == "" {
			c.problemf("%s: a blank fragment would match every identity", field)
			continue
		}
		out = append(out, strings.ToLower(s))
	}
	return out
}

// emails lowercases whole email addresses. Co-author rules match an email
// exactly, so a fragment such as "noreply" is refused rather than matched
// loosely: people use GitHub noreply addresses.
func (c *checker) emails(field string, in []string) []string {
	var out []string
	for _, s := range in {
		local, domainPart, ok := strings.Cut(s, "@")
		if !ok || local == "" || domainPart == "" || strings.Contains(domainPart, "@") || strings.ContainsFunc(s, unicode.IsSpace) {
			c.problemf("%s: %q is not an email address; co-author rules match whole emails, never a fragment", field, s)
			continue
		}
		out = append(out, strings.ToLower(s))
	}
	return out
}

// roles builds the [roles] table: extension and name maps keyed for lookup,
// and the path rules in order.
func (c *checker) roles(f rolesFile) Roles {
	var r Roles
	r.Extensions = c.roleMap("[roles.extensions]", f.Extensions, func(ext string) (string, string) {
		switch {
		case len(ext) < 2 || ext[0] != '.':
			return "", "want a dot and at least one character, such as \".go\""
		case strings.ContainsAny(ext, "/\x00") || strings.ContainsFunc(ext, unicode.IsSpace):
			return "", "an extension holds no slash or space"
		case strings.Count(ext, ".") > 1:
			// classify reads only the name's last dot suffix, so the entry
			// would never match.
			return "", "an extension is the name's last dot suffix, with one dot, such as \".gz\"; a [[roles.path]] pattern matches a longer suffix"
		}
		return strings.ToLower(ext), ""
	})
	r.Names = c.roleMap("[roles.names]", f.Names, func(name string) (string, string) {
		if name == "" || strings.ContainsAny(name, "/\x00") {
			return "", "want a file name, without a slash"
		}
		return name, ""
	})
	for i, rule := range f.Path {
		where := fmt.Sprintf("[[roles.path]] rule %d", i+1)
		pr := PathRule{Role: domain.Role(rule.Role)}
		if !isRole(rule.Role) {
			c.problemf("%s: role %q is not a role (want one of %s)", where, rule.Role, roleList())
		}
		pr.Pattern = c.pattern(where, rule.Pattern)
		// max_bytes is optional, as when is, so a policy written before it
		// (a user's file, a stored run's source) reads as it always did.
		if rule.MaxBytes != nil {
			if *rule.MaxBytes < 1 {
				c.problemf("%s: max_bytes = %d: want 1 or more", where, *rule.MaxBytes)
			}
			pr.MaxBytes = *rule.MaxBytes
		}
		for _, w := range rule.When {
			if !isRole(w) {
				c.problemf("%s: when %q is not a role (want one of %s)", where, w, roleList())
				continue
			}
			pr.When = append(pr.When, domain.Role(w))
		}
		r.Path = append(r.Path, pr)
	}
	return r
}

// ruleNotes reads, for each [[roles.path]] table of a policy's TOML in
// order, the first sentence of the comment inside it, up to a colon
// (PathRule.Note): the comment lines between its header and the next
// table's, joined. A table without a comment has "". The caller uses the
// notes only when there is one for every rule, so a file that writes its
// rules another way (an inline array of tables) gets none rather than
// another rule's.
func ruleNotes(tomlText string) []string {
	var notes []string
	var text []string
	in := false
	flush := func() {
		if in {
			s := strings.Join(text, " ")
			for _, sep := range []string{": ", ". "} {
				if i := strings.Index(s, sep); i >= 0 {
					s = s[:i]
				}
			}
			notes = append(notes, strings.TrimSuffix(s, "."))
		}
		text = nil
	}
	for _, line := range strings.Split(tomlText, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "["):
			flush()
			in = strings.ReplaceAll(line, " ", "") == "[[roles.path]]"
		case in && strings.HasPrefix(line, "#"):
			text = append(text, strings.TrimSpace(strings.TrimLeft(line, "#")))
		}
	}
	flush()
	return notes
}

// roleMap inverts a role → keys table into the key → role map the
// classifier looks up. norm checks and normalises one key, returning why it
// is refused. A key listed under two roles is a problem: the classifier
// could not say which role it has.
func (c *checker) roleMap(table string, byRole map[string][]string, norm func(string) (string, string)) map[string]domain.Role {
	var out map[string]domain.Role
	// Sorted, so a key listed twice is always reported against the same
	// pair of roles.
	for _, role := range slices.Sorted(maps.Keys(byRole)) {
		for _, raw := range byRole[role] {
			key, why := norm(raw)
			if why != "" {
				c.problemf("%s %s: %q: %s", table, role, raw, why)
				continue
			}
			if prev, ok := out[key]; ok {
				if prev != domain.Role(role) {
					c.problemf("%s: %q is listed for both %s and %s", table, raw, prev, role)
				}
				continue
			}
			if out == nil {
				out = map[string]domain.Role{}
			}
			out[key] = domain.Role(role)
		}
	}
	return out
}

// paths builds the [paths] table: exactly one of deny and package_file,
// as from allows.
func (c *checker) paths(f pathsFile, denyText []byte, from origin) Paths {
	hasDeny := c.md.IsDefined("paths", "deny")
	hasPackage := c.md.IsDefined("paths", "package_file")
	switch {
	case hasDeny && hasPackage:
		c.problemf("[paths] sets both deny and package_file: set deny alone")
		return Paths{}
	case !hasDeny && !hasPackage && from == fromPackage:
		c.problemf("[paths] has no package_file")
		return Paths{}
	case !hasDeny && !hasPackage:
		c.problemf("missing key [paths] deny")
		return Paths{}
	case hasPackage && from == fromUser:
		c.problemf("[paths] package_file names a file inside stillhere, which only the packaged policy may use: copy the patterns you want from %s into [paths] deny", packageFile)
		return Paths{}
	case hasDeny && from == fromPackage:
		c.problemf("[paths] deny: the packaged policy keeps its deny list in %s", packageFile)
		return Paths{}
	case hasPackage && from == fromPackage && f.PackageFile != packageFile:
		c.problemf("[paths] package_file %q: the packaged deny list is %s", f.PackageFile, packageFile)
		return Paths{}
	case hasPackage && f.PackageFile == "":
		c.problemf("[paths] package_file is empty")
		return Paths{}
	case hasDeny && len(denyText) > 0:
		// Only a stored source can get here: it carries deny list text,
		// but its policy keeps the deny list in the TOML.
		c.problemf("the policy sets [paths] deny, but its source also carries %d bytes of deny list text", len(denyText))
		return Paths{}
	}
	ps := Paths{Source: pathsFromPolicy}
	if hasDeny {
		for i, s := range f.Deny {
			if re := c.pattern(fmt.Sprintf("[paths] deny pattern %d", i+1), s); re != nil {
				ps.Deny = append(ps.Deny, re)
			}
		}
	} else {
		ps.Deny, ps.origins = c.denyList(f.PackageFile, string(denyText))
		ps.Source = pathsFromPackage + f.PackageFile
	}
	for _, re := range ps.Deny {
		ps.filter = append(ps.filter, newDenyFilter(re))
	}
	return ps
}

// denyList compiles a package file's deny list text: one RE2 pattern per
// line; blank lines and lines whose first non-blank character is # are
// comments, and surrounding whitespace is ignored. Each pattern's origin
// is its line and the label of the comment block that opens its group: the
// first line of the block that says something (denyNote), for every
// pattern until a blank line ends the group.
func (c *checker) denyList(name, text string) ([]*regexp.Regexp, []denyOrigin) {
	var deny []*regexp.Regexp
	var origins []denyOrigin
	note, block := "", false // the group's label; whether the line above was a comment
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			note, block = "", false
			continue
		case strings.HasPrefix(line, "#"):
			if !block {
				note = ""
			}
			if note == "" {
				note = denyNote(line)
			}
			block = true
			continue
		}
		block = false
		if re := c.pattern(fmt.Sprintf("%s line %d", name, i+1), line); re != nil {
			deny = append(deny, re)
			origins = append(origins, denyOrigin{line: i + 1, note: note})
		}
	}
	return deny, origins
}

// pattern compiles one RE2 path pattern. An empty pattern would match every
// path, and a pattern RE2 rejects, such as Linguist's lookarounds, is never
// loaded: there is no fallback engine.
func (c *checker) pattern(where, s string) *regexp.Regexp {
	if s == "" {
		c.problemf("%s: an empty pattern would match every path", where)
		return nil
	}
	re, err := regexp.Compile(s)
	if err != nil {
		c.problemf("%s %q: %v", where, s, err)
		return nil
	}
	return re
}

// isRole reports whether s names one of the classifier's roles.
func isRole(s string) bool {
	switch domain.Role(s) {
	case domain.RoleProd, domain.RoleSQL, domain.RoleMarkup, domain.RoleIaC,
		domain.RoleDoc, domain.RoleTest, domain.RoleMigration, domain.RoleSeed:
		return true
	}
	return false
}

// roleList names the roles for messages.
func roleList() string {
	return strings.Join([]string{
		string(domain.RoleProd), string(domain.RoleSQL), string(domain.RoleMarkup), string(domain.RoleIaC),
		string(domain.RoleDoc), string(domain.RoleTest), string(domain.RoleMigration), string(domain.RoleSeed),
	}, ", ")
}

// keyName renders a key path as a reader finds it in the file: "[week]
// days" for a key in a table, "[roles.path] rule" for a key in an array of
// tables, and the bare name at the top level.
func keyName(k []string) string {
	if len(k) == 1 {
		return quoteKey(k[0])
	}
	return "[" + dotted(k[:len(k)-1]) + "] " + quoteKey(k[len(k)-1])
}

// dotted joins a key path the way a TOML table header writes it.
func dotted(k []string) string {
	parts := make([]string, len(k))
	for i, part := range k {
		parts[i] = quoteKey(part)
	}
	return strings.Join(parts, ".")
}

// quoteKey quotes a key part that is not a bare TOML key (ASCII letters,
// digits, "_" and "-"), so a message shows it unambiguously.
func quoteKey(s string) string {
	bare := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !bare(r) }) < 0 {
		return s
	}
	return fmt.Sprintf("%q", s)
}
