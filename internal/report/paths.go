package report

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// The paths answer (stillhere paths): every path a run classified, with the
// class the classifier gave it and the rule that decided, so a reader can
// check what counts as product code without reading facts.db. The rules
// come from classify.Explain over the run's own policy; this file arranges
// and prints them.

// Paths is the paths answer.
type Paths struct {
	Provenance Provenance `json:"provenance"`
	Prefixes   []string   `json:"prefixes"` // the PATH prefixes asked about, repo-relative; [] for every path
	Repo       string     `json:"repo"`     // the --repo filter (Repo.Key), or "" for every repository
	Role       string     `json:"role"`     // the --role filter, or ""
	Skipped    bool       `json:"skipped"`  // --skipped: the skipped paths only
	// PolicySource is where the run's policy came from: "packaged policy",
	// or the policy file's name as the scan read it, such as
	// "stillhere.toml"; the hash is the provenance's.
	PolicySource string      `json:"policy_source"`
	Repos        []PathsRepo `json:"repos"`
	// Rules are the rules that decided the listed paths, each once, with
	// how many of them it decided: the legend the text and Markdown forms
	// print below the paths.
	Rules   []PathRuleUse `json:"rules"`
	Caveats []Caveat      `json:"caveats"`
}

// PathsRepo is the listed paths of one repository.
type PathsRepo struct {
	Repo string `json:"repo"` // name
	Key  string `json:"key"`
	// Classified counts every path the run classified in the repository,
	// at HEAD or touched by the history, before the filters: the scope of
	// the card's "skipped paths".
	Classified int `json:"classified"`
	// Counts counts the listed paths by class: a role, or "skipped:" and
	// the skip reason, such as "skipped:denied_path".
	Counts map[string]int `json:"counts"`
	// HistoryOnly counts the listed paths that HEAD no longer has, which
	// only the history touched.
	HistoryOnly int       `json:"history_only"`
	Paths       []PathRow `json:"paths"` // sorted by path
}

// PathRow is one path and what decided its class.
type PathRow struct {
	Path   string   `json:"path"`
	Role   string   `json:"role"`    // "" when skipped
	Skip   string   `json:"skip"`    // denied_path, too_big, generated_header or unclassified; "" when it has a role
	AtHead bool     `json:"at_head"` // false: HEAD no longer has it, and only the history touched it
	Size   int64    `json:"size"`    // bytes the too_big check read; -1 when unknown
	Rule   PathRule `json:"rule"`
}

// PathRule is the rule that decided a path's class (classify.Rule).
type PathRule struct {
	// Kind is the classifier's step: deny, attribute, size, header,
	// not-file, path, name, extension, none, or unknown for a class an
	// older stillhere gave that this one would not.
	Kind  string `json:"kind"`
	Text  string `json:"text"`  // the rule as the policy writes it: a pattern, ".go", "Dockerfile", "max_bytes = 400000"
	Where string `json:"where"` // where the policy holds it, such as "[[roles.path]] rule 4" or "generated_paths.txt line 478"
	// Source is where the rule lives: "packaged policy", the policy file's
	// name, "linguist attribute", "generated header" or "file type".
	Source string `json:"source"`
	// Note is why the rule exists, when anything says: the policy's own
	// comment for it, the deny list's label; the same for every path the
	// rule decides.
	Note string `json:"note"`
	// Detail is what this path adds, not the rule: its size for a size
	// bound, or the deny pattern .gitattributes overrode; "" when nothing.
	// The legend of the rules leaves it out.
	Detail string `json:"detail"`
}

// PathRuleUse is one rule of the legend, and how many listed paths it
// decided.
type PathRuleUse struct {
	PathRule
	Class string `json:"class"` // the class it gave: a role, or "skipped:" and the reason
	Paths int    `json:"paths"`
}

// PathsInput is what NewPaths arranges: the run's facts, the paths asked
// about with their rules, already explained, and the filters.
type PathsInput struct {
	Run          *domain.Run
	Facts        *domain.FactSet // the repositories asked about, whose provenance and caveats the answer carries
	Prefixes     []string
	Repo         string
	Role         string
	Skipped      bool
	PolicySource string
	// Rows are the paths of each repository that pass the filters, keyed
	// by Repo.Key, each with its rule.
	Rows map[string][]PathRow
}

// SkipClass is how the paths answer names a skipped class: "skipped:" and
// the reason, so a role and a skip reason never collide in Counts.
func SkipClass(s domain.Skip) string { return "skipped:" + string(s) }

// NewPaths builds the paths answer.
func NewPaths(in *PathsInput) *Paths {
	ps := &Paths{
		Provenance: AnswerProvenance(in.Run, in.Facts, nil, nil),
		Prefixes:   slices.Clone(in.Prefixes), Repo: in.Repo, Role: in.Role, Skipped: in.Skipped,
		PolicySource: in.PolicySource,
		Caveats:      AnswerCaveats(in.Facts),
	}
	if ps.Prefixes == nil {
		ps.Prefixes = []string{}
	}
	type ruleKey struct{ class, kind, text, where, source string }
	uses := map[ruleKey]*PathRuleUse{}
	for i := range in.Facts.Repos {
		rf := &in.Facts.Repos[i]
		r := PathsRepo{Repo: rf.Repo.Name, Key: rf.Repo.Key, Classified: len(rf.Paths), Counts: map[string]int{},
			Paths: slices.Clone(in.Rows[rf.Repo.Key])}
		if r.Paths == nil {
			r.Paths = []PathRow{}
		}
		slices.SortFunc(r.Paths, func(a, b PathRow) int { return cmp.Compare(a.Path, b.Path) })
		for _, row := range r.Paths {
			class := pathClass(row)
			r.Counts[class]++
			if !row.AtHead {
				r.HistoryOnly++
			}
			k := ruleKey{class, row.Rule.Kind, row.Rule.Text, row.Rule.Where, row.Rule.Source}
			u, ok := uses[k]
			if !ok {
				u = &PathRuleUse{PathRule: row.Rule, Class: class}
				u.Detail = "" // a fact of the row's path, not of the rule
				uses[k] = u
			}
			u.Paths++
		}
		ps.Repos = append(ps.Repos, r)
	}
	slices.SortFunc(ps.Repos, func(a, b PathsRepo) int { return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Key, b.Key)) })
	for _, u := range uses {
		ps.Rules = append(ps.Rules, *u)
	}
	slices.SortFunc(ps.Rules, func(a, b PathRuleUse) int {
		return cmp.Or(cmp.Compare(b.Paths, a.Paths), cmp.Compare(a.Class, b.Class), cmp.Compare(a.Where, b.Where), cmp.Compare(a.Text, b.Text))
	})
	if ps.Repos == nil {
		ps.Repos = []PathsRepo{}
	}
	if ps.Rules == nil {
		ps.Rules = []PathRuleUse{}
	}
	return ps
}

// pathClass is a row's class: its role, or SkipClass of its reason.
func pathClass(r PathRow) string {
	if r.Role != "" {
		return r.Role
	}
	return SkipClass(domain.Skip(r.Skip))
}

// classLabel is how text and Markdown print a class: the role, or the skip
// reason in the words of a card's "skipped paths".
func classLabel(class string) string {
	switch class {
	case SkipClass(domain.SkipDeniedPath):
		return "denied"
	case SkipClass(domain.SkipTooBig):
		return "too big"
	case SkipClass(domain.SkipGeneratedHeader):
		return "generated header"
	case SkipClass(domain.SkipUnclassified):
		return "unclassified"
	}
	return class
}

// ruleRef is how a row names its rule: where the policy holds it, else what
// the step found.
func ruleRef(r PathRule) string {
	if r.Where != "" {
		return r.Where
	}
	return r.Text
}

// countsText is "177 prod, 64 doc, 25 iac · skipped 38 denied, 2 too big"
// for a repository's listed paths, roles first by count, then the skip
// reasons in the classifier's order.
func countsText(r PathsRepo) string {
	type kv struct {
		class string
		n     int
	}
	var roles []kv
	for class, n := range r.Counts {
		if !strings.HasPrefix(class, "skipped:") {
			roles = append(roles, kv{class, n})
		}
	}
	slices.SortFunc(roles, func(a, b kv) int { return cmp.Or(cmp.Compare(b.n, a.n), cmp.Compare(a.class, b.class)) })
	var parts []string
	for _, x := range roles {
		parts = append(parts, fmt.Sprintf("%s %s", grouped(x.n), x.class))
	}
	var skipped []string
	for _, s := range []domain.Skip{domain.SkipDeniedPath, domain.SkipTooBig, domain.SkipGeneratedHeader, domain.SkipUnclassified} {
		if n := r.Counts[SkipClass(s)]; n > 0 {
			skipped = append(skipped, grouped(n)+" "+classLabel(SkipClass(s)))
		}
	}
	out := strings.Join(parts, ", ")
	if len(skipped) > 0 {
		if out != "" {
			out += " · "
		}
		out += "skipped " + strings.Join(skipped, ", ")
	}
	if out == "" {
		out = "no path passes the filters"
	}
	return out
}

// pathsHeading is "paths · run 7 · as of ... · policy 0123456789ab
// (packaged revision 3)", or the policy file's name after its hash, and
// the filters.
func (ps *Paths) pathsHeading() string {
	s := "paths · " + provenanceText(ps.Provenance)
	if ps.Provenance.PolicyRevision == 0 { // a revision names the packaged policy already
		s += " (" + ps.PolicySource + ")"
	}
	var filters []string
	if len(ps.Prefixes) > 0 {
		filters = append(filters, "under "+strings.Join(ps.Prefixes, ", "))
	}
	if ps.Role != "" {
		filters = append(filters, "role "+ps.Role)
	}
	if ps.Skipped {
		filters = append(filters, "skipped only")
	}
	if len(filters) > 0 {
		s += " · " + strings.Join(filters, " · ")
	}
	return s
}

// repoLine is a repository's heading: its listed paths and how many of
// the classified ones they are.
func repoLine(r PathsRepo) string {
	s := fmt.Sprintf("%s of %s classified paths: %s", grouped(len(r.Paths)), grouped(r.Classified), countsText(r))
	if r.HistoryOnly > 0 {
		s += fmt.Sprintf(" · %s only in the history, not at HEAD", grouped(r.HistoryOnly))
	}
	return s
}

func (ps *Paths) writeText(p *printer) {
	p.wrapped("", "  ", ps.pathsHeading())
	p.wrapped("  ", "  ", "each path's class and the rule that decided it, from the run's own policy; rules are listed below")
	for _, r := range ps.Repos {
		p.blank()
		p.wrapped("", "  ", "Repository "+clean(r.Repo)+" · "+repoLine(r))
		if len(r.Paths) == 0 {
			continue
		}
		classW, ruleW := utf8.RuneCountInString("CLASS"), utf8.RuneCountInString("RULE")
		for _, row := range r.Paths {
			classW = max(classW, utf8.RuneCountInString(classLabel(pathClass(row))))
			ruleW = max(ruleW, utf8.RuneCountInString(clean(ruleRef(row.Rule))))
		}
		pad := func(s string, w int) string { return s + strings.Repeat(" ", w-utf8.RuneCountInString(s)) }
		p.line("  " + pad("CLASS", classW) + "  " + pad("RULE", ruleW) + "  PATH")
		for _, row := range r.Paths {
			path := clean(row.Path)
			if !row.AtHead {
				path += "  (history only)"
			}
			if row.Rule.Detail != "" {
				path += "  (" + clean(row.Rule.Detail) + ")"
			}
			p.line("  " + pad(classLabel(pathClass(row)), classW) + "  " + pad(clean(ruleRef(row.Rule)), ruleW) + "  " + path)
		}
	}
	if len(ps.Rules) > 0 {
		p.blank()
		p.line("Rules that decided these paths")
		for _, u := range ps.Rules {
			p.line(fmt.Sprintf("  %s · %s · %s (%s)", clean(ruleRef(u.PathRule)), classLabel(u.Class),
				plural(u.Paths, "path", "paths"), clean(u.Source)))
			if u.Where != "" && u.Text != "" {
				p.line("      " + clean(u.Text))
			}
			if u.Note != "" {
				p.wrapped("      ", "      ", clean(u.Note))
			}
		}
	}
	textCaveats(p, "", "What could make this wrong", ps.Caveats, false)
}

func (ps *Paths) writeMarkdown(p *printer) {
	p.line("# Paths")
	p.blank()
	p.line("- " + mdText(strings.TrimPrefix(ps.pathsHeading(), "paths · ")))
	p.line("- Each path's class and the rule that decided it, from the run's own policy; the rules are listed below.")
	for _, r := range ps.Repos {
		p.blank()
		p.line("## Repository " + mdText(r.Repo))
		p.blank()
		p.line("- " + mdText(repoLine(r)))
		if len(r.Paths) == 0 {
			continue
		}
		p.blank()
		p.line("| Class | Rule | Path |")
		p.line("| --- | --- | --- |")
		for _, row := range r.Paths {
			path := mdCode(row.Path)
			if !row.AtHead {
				path += " (history only)"
			}
			if row.Rule.Detail != "" {
				path += " (" + mdText(row.Rule.Detail) + ")"
			}
			p.line("| " + classLabel(pathClass(row)) + " | " + mdText(ruleRef(row.Rule)) + " | " + path + " |")
		}
	}
	if len(ps.Rules) > 0 {
		p.blank()
		p.line("## Rules that decided these paths")
		p.blank()
		p.line("| Rule | Class | Paths | Source | Pattern or value | Why |")
		p.line("| --- | --- | ---: | --- | --- | --- |")
		for _, u := range ps.Rules {
			text := ""
			if u.Where != "" {
				text = mdCode(u.Text)
			}
			p.line(fmt.Sprintf("| %s | %s | %d | %s | %s | %s |", mdText(ruleRef(u.PathRule)), classLabel(u.Class), u.Paths,
				mdText(u.Source), text, mdText(u.Note)))
		}
	}
	mdCaveats(p, "##", ps.Caveats, false)
}
