package metrics

import (
	"github.com/IHaveASegway/stillhere/internal/domain"
)

// survivingV1 is surviving@1: whose non-blank lines are still on the
// default branch, credited by identity.Credit over the blamed commit's
// author and co-authors. It also carries the context numbers that describe
// a person's shape (breadth, depth, the role mix, activity) and a code
// population's makeup (size, people, the bot and AI-assisted shares). They
// are context: surviving lines reward tenure, so nothing here is ranked.
type survivingV1 struct{}

var survivingID = domain.MetricID{Name: "surviving", Version: 1}

// Definition returns surviving@1's catalog entry.
func (survivingV1) Definition() Definition {
	return Definition{
		ID:       survivingID,
		Question: "Whose lines are still on the default branch?",
		Unit: "Non-blank prod lines at HEAD per person, per repository, group and all, credited to the blamed commit's " +
			"author or, for an agent's commit, its first human co-author. A person has a value in a scope when they hold prod " +
			"lines there. Detail per person: repos, majority_files, lines_<role> for each role with lines, landings_365d (the " +
			"landings in [now - 365d, now) landed@2 credits to them: holding a non-merge commit of theirs that is no copy) and " +
			"merged_365d (the merge landings of that year whose landing commit is theirs, landed@2's merged). Per repository " +
			"and group (subject repo or group): prod_lines, prod_files, people, bot_share, ai_assisted_share.",
		Ranked:      false,
		Subjects:    []string{domain.SubjectPerson, domain.SubjectRepo, domain.SubjectGroup},
		Eligibility: "Not ranked: surviving lines reward tenure.",
		BlindSpots: "Blame credits the last author of a line, so a reformat not listed in .git-blame-ignore-revs moves lines to its author. " +
			"Moves across files credit the mover (no -C). A squash merge credits every line of a pull request to its author, and its " +
			"co-authors get none. Repositories without blame (shallow or partial clones) have no value rather than zero.",
		Gaming:   "Rewriting other people's lines (a reformat, a rename of every identifier) moves their blame to the rewriter.",
		Oracle:   "testdata/fixture/SPEC.md#still-here-oracle-m1-teststillhereoracle",
		Validity: survivingValidity,
	}
}

// survivingValidity is the evidence behind blame-based authorship, shared by
// the metrics that read surviving lines.
const survivingValidity = "Context, never ranked. Evidence for blame as an authorship signal: Avelino, Passos, Petrillo and Valente, " +
	"\"Who Can Maintain This Code?\" (IEEE Software 36(6), 2019; SBES 2018), compared developer-named maintainers of ten systems " +
	"with three repository-mining techniques. On the two commercial systems the share of lines a developer owns in the last " +
	"version (blame) had the best hit rate (harmonic mean 67%, against 66% for commits and 63% for degree of authorship), and on " +
	"eight open-source systems all three were within 60-63%. Their threshold was relative to a file's top author (best at 10%), " +
	"not stillhere's half of the file, so they support the signal, not this exact rule."

// Compute returns surviving@1's values.
func (m survivingV1) Compute(in *Input) ([]domain.MetricValue, error) { return computeAlone(m, in) }

func (survivingV1) compute(v *view) ([]domain.MetricValue, error) {
	year := v.window(yearDays, 0)
	// landings_365d and merged_365d, by landed@2's credit (landedV2Parts):
	// who wrote what landed, so a person who merges is not credited with
	// the merged branch, and the merges they made are counted apart, as
	// landed@2's merged. It is activity, so every landing seen in the year
	// counts, covered or not (repoView.covers).
	landings := make(map[personKey]int)
	merged := make(map[personKey]int)
	for _, r := range v.repos {
		for _, l := range r.landings {
			if l.commit == nil || !year.Contains(l.time) {
				continue
			}
			for _, p := range landedV2Parts(r, l).people {
				for _, s := range r.scopes {
					landings[personKey{s, p.email}]++
				}
			}
			if l.commit.merge {
				for _, s := range r.scopes {
					merged[personKey{s, l.commit.credit}]++
				}
			}
		}
	}

	type acc struct {
		roles           map[domain.Role]int
		majority, repos int
	}
	accs := make(map[personKey]*acc)
	for _, r := range v.repos {
		if !r.lines {
			continue
		}
		byPerson := make(map[string]map[domain.Role]int)
		for _, o := range r.owners {
			roles := byPerson[o.credit]
			if roles == nil {
				roles = make(map[domain.Role]int)
				byPerson[o.credit] = roles
			}
			roles[o.role] += o.lines
		}
		majority := make(map[string]int)
		for _, f := range r.files {
			for _, email := range f.owners {
				majority[email]++
			}
		}
		for email, roles := range byPerson {
			for _, s := range r.scopes {
				a := lookup(accs, personKey{s, email})
				if a.roles == nil {
					a.roles = make(map[domain.Role]int, len(roles))
				}
				for role, n := range roles {
					a.roles[role] += n
				}
				a.majority += majority[email]
				if roles[domain.RoleProd] > 0 {
					a.repos++
				}
			}
		}
	}

	var out []domain.MetricValue
	for k, a := range accs {
		// The still-here table lists the people who hold prod lines in the
		// scope; someone whose lines there are all tests or docs has no
		// row, as in the fixture's oracle, and a row's Detail carries the
		// lines of every role.
		if a.roles[domain.RoleProd] == 0 {
			continue
		}
		detail := map[string]float64{
			"repos":          float64(a.repos),
			"majority_files": float64(a.majority),
			"landings_365d":  float64(landings[k]),
			"merged_365d":    float64(merged[k]),
		}
		for role, n := range a.roles {
			detail["lines_"+string(role)] = float64(n)
		}
		out = append(out, count(survivingID, k.scope, personSubject(k.email), domain.Window{}, a.roles[domain.RoleProd], 0, detail))
	}
	return sorted(append(out, survivingPopulations(v, year)...)), nil
}

// population is the makeup of a set of repositories: what the issue calls
// the numbers an agent needs to describe a platform or a repo.
type population struct {
	lines              bool // at least one repository had blame
	prodLines, files   int
	botLines           int
	people             map[string]bool // humans with surviving prod lines
	landings, aiAssist int             // landings in the last 365 days, and the AI-assisted ones
}

// add folds one repository into the population.
func (p *population) add(v *view, r *repoView, year domain.Window) {
	for _, l := range r.landings {
		if r.covers(year) && year.Contains(l.time) {
			p.landings++
			if l.ai {
				p.aiAssist++
			}
		}
	}
	if !r.lines {
		return
	}
	p.lines = true
	if p.people == nil {
		p.people = make(map[string]bool)
	}
	for _, f := range r.files {
		p.files++
		p.prodLines += f.total
		for email, n := range f.byPerson {
			if v.who.Bot(email) {
				p.botLines += n
			} else {
				p.people[email] = true
			}
		}
	}
}

// value renders the population as a surviving@1 value about sub in scope s.
func (p *population) value(s domain.Scope, sub domain.Subject) domain.MetricValue {
	detail := map[string]float64{
		"prod_lines": float64(p.prodLines),
		"prod_files": float64(p.files),
		"people":     float64(len(p.people)),
	}
	if p.prodLines > 0 {
		detail["bot_share"] = float64(p.botLines) / float64(p.prodLines)
	}
	if p.landings > 0 {
		detail["ai_assisted_share"] = float64(p.aiAssist) / float64(p.landings)
	}
	return count(survivingID, s, sub, domain.Window{}, p.prodLines, 0, detail)
}

// survivingPopulations returns one value per repository with blame and one
// per group, about the repository or group itself. A group's numbers are
// recomputed over its repositories (people are distinct humans across
// them), never summed from the per-repository shares.
func survivingPopulations(v *view, year domain.Window) []domain.MetricValue {
	var out []domain.MetricValue
	groups := make(map[string]*population)
	for _, r := range v.repos {
		var p population
		p.add(v, r, year)
		if p.lines {
			out = append(out, p.value(domain.Scope{Kind: domain.ScopeRepo, Key: r.key}, repoSubject(r.key)))
		}
		if r.group != "" {
			lookup(groups, r.group).add(v, r, year)
		}
	}
	for name, p := range groups {
		if p.lines {
			out = append(out, p.value(domain.Scope{Kind: domain.ScopeGroup, Key: name}, groupSubject(name)))
		}
	}
	return out
}
