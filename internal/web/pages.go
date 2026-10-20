package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/metrics"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// The pages, each under the server's base ("/" + token + "/"). Each reads
// one use case, as the command of the same name does, and every page of a
// run carries the run's id in its path, so a link keeps naming the run it
// was made from after a scan publishes another; the base alone is the
// latest.
//
//	BASE                    the latest complete or partial scan's page
//	BASE runs               every stored scan run
//	BASE metrics            every metric's definition (stillhere metrics)
//	BASE run/ID/            the run: its caveats and overviews (stillhere report)
//	BASE run/ID/ranks       its rank tables, every ranked metric's (report --rank)
//	BASE run/ID/repo?key=K  one repository's section of the report
//	BASE run/ID/person?who=W  a person's own page (stillhere me)
//	BASE run/ID/explain?metric=M&person=W|repo=K|group=G|area=K:DIR|all=1[&days=N]
//	BASE run/ID/who-knows?path=P[&repo=K][&days=N]
//
// A person is linked by a handle of the server's own (handle); a page
// asked about a person by anything else (a display name, an email, an id)
// answers with a redirect to the same page by their handle, so no address
// stays in an address. Every table of a page is also its CSV: the page's
// address with csv=N, N counting the page's tables from 1.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.HandlerFunc(s.latest))
	mux.Handle("GET /runs", s.serve(s.runsPage))
	mux.Handle("GET /metrics", s.serve(s.metricsPage))
	mux.Handle("GET /run/{id}/{$}", s.serve(s.runPage))
	mux.Handle("GET /run/{id}/ranks", s.serve(s.ranksPage))
	mux.Handle("GET /run/{id}/repo", s.serve(s.repoPage))
	mux.Handle("GET /run/{id}/person", s.serve(s.personPage))
	mux.Handle("GET /run/{id}/explain", s.serve(s.explainPage))
	mux.Handle("GET /run/{id}/who-knows", s.serve(s.whoKnowsPage))
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	return mux
}

// latest sends the reader to the latest complete or partial scan's page,
// read at each request, so a run a scan publishes is the next one shown.
func (s *Server) latest(w http.ResponseWriter, r *http.Request) {
	release, ok := s.hold(r.Context())
	if !ok {
		return
	}
	run, err := s.d.Store.LatestRun(r.Context(), domain.RunKindScan)
	release()
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			err = &domain.UsageError{Msg: "facts.db holds no complete or partial scan yet: run 'stillhere scan PATH' " +
				"and open this page again"}
		}
		s.write(w, r, errorPage(err))
		return
	}
	to := s.runPath(run.ID, "")
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.Query().Encode() // csv= among it
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// runPath is the address of a page of run id: "" for the run's own.
func (s *Server) runPath(id int64, page string) string {
	return s.base + "run/" + strconv.FormatInt(id, 10) + "/" + page
}

// runID reads the run id a page's path names, and the run, which the page
// heads itself with.
func (s *Server) runID(r *http.Request) (domain.Run, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return domain.Run{}, &domain.UsageError{Msg: fmt.Sprintf("%q is not a run id: the runs page lists them",
			r.PathValue("id"))}
	}
	run, err := s.d.Store.Run(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Run{}, fmt.Errorf("facts.db holds no complete or partial run %d (the runs page lists those it "+
			"holds): %w", id, domain.ErrNotFound)
	}
	if err == nil && run.Kind != domain.RunKindScan {
		return domain.Run{}, fmt.Errorf("run %d is a %s run, which has no report (the runs page lists the scans): %w",
			id, run.Kind, domain.ErrNotFound)
	}
	return run, err
}

// model returns run id's report with every ranked metric ranked
// (app.RankedMetrics, the ones a person's own page ranks too), built once:
// a published run never changes, so neither does its report. The last two
// runs' reports are kept, as the run cache keeps their facts. The caller
// holds the slot.
func (s *Server) model(ctx context.Context, id int64) (*report.Model, error) {
	if i := slices.Index(s.order, id); i >= 0 {
		s.order = append(slices.Delete(s.order, i, i+1), id)
		return s.models[id], nil
	}
	m, err := app.Report(ctx, s.d, app.ReportRequest{RunID: id, Rank: app.RankedMetrics()})
	if err != nil {
		return nil, err
	}
	s.models[id] = m
	s.order = append(s.order, id)
	for len(s.order) > 2 {
		delete(s.models, s.order[0])
		s.order = s.order[1:]
	}
	return m, nil
}

// handle is a person's handle on this server's pages: a keyed hash of
// their key (their email, or @login), recorded so a page that gets it
// back knows whom it names. The caller holds the slot.
func (s *Server) handle(key string) string {
	mac := hmac.New(sha256.New, s.hkey)
	mac.Write([]byte(key))
	h := "u-" + hex.EncodeToString(mac.Sum(nil)[:8])
	s.people[h] = key
	return h
}

// who is what a page asks the use cases about a person named w by: their
// key when w is one of the server's handles, else w as typed, which the
// use cases resolve (a display name, an email, an id or @login). The
// caller holds the slot.
func (s *Server) who(w string) string {
	if key, ok := s.people[w]; ok {
		return key
	}
	return w
}

// links is how a page of run id links what it names (report.Links): each
// person to their own page by their handle, each repository to its
// section, each area to who knows it, and each ranked estimate to the
// changes behind it. seen, when not nil, records each repository linked.
// The caller holds the slot.
func (s *Server) links(id int64, seen map[string]bool) report.Link {
	return func(t report.Target) string {
		switch t.Kind {
		case report.TargetPerson:
			return s.runPath(id, "person") + "?" + url.Values{"who": {s.handle(t.Key)}}.Encode()
		case report.TargetRepo:
			if seen != nil {
				seen[t.Key] = true
			}
			return s.runPath(id, "repo") + "?" + url.Values{"key": {t.Key}}.Encode()
		case report.TargetArea:
			return s.runPath(id, "who-knows") + "?" + url.Values{"repo": {t.Repo}, "path": {t.Path}}.Encode()
		case report.TargetExplain:
			return s.runPath(id, "explain") + "?" +
				url.Values{"metric": {t.Metric}, "person": {s.handle(t.Key)}}.Encode()
		}
		return ""
	}
}

// markdown renders d as a page of run id renders it: every row of every
// table, sorted and filtered in the browser, and linked.
func (s *Server) markdown(id int64, d report.Document) ([]byte, error) {
	var b bytes.Buffer
	if err := report.Markdown(&b, d, report.AllRows(), report.Links(s.links(id, nil))); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// trace links each metric of held, the metric@version ids of the numbers a
// page shows about a subject of kind (its section's provenance, or the
// person's values), to explain's page for it, with q naming the subject:
// the ones the registry says explain answers about that kind
// (metrics.Definition.Subjects), without the one only a person's own page
// shows (report.OwnPageOnly).
func (s *Server) trace(id int64, kind string, held []string, q url.Values) []traceLink {
	var out []traceLink
	for _, name := range held {
		m, err := metrics.Lookup(name)
		if err != nil {
			continue
		}
		def := m.Definition()
		if !slices.Contains(def.Subjects, kind) || (kind == domain.SubjectPerson && report.OwnPageOnly(def.ID)) {
			continue
		}
		v := url.Values{"metric": {def.ID.String()}}
		for k, vs := range q {
			v[k] = vs
		}
		out = append(out, traceLink{Metric: def.ID.String(), URL: s.runPath(id, "explain") + "?" + v.Encode()})
	}
	return out
}

func (s *Server) runPage(r *http.Request) (*page, error) {
	run, err := s.runID(r)
	if err != nil {
		return nil, err
	}
	m, err := s.model(r.Context(), run.ID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var b bytes.Buffer
	if err := report.MarkdownRun(&b, m, report.AllRows(), report.Links(s.links(run.ID, seen))); err != nil {
		return nil, err
	}
	p := &page{Title: fmt.Sprintf("Run %d", run.ID), Run: &run, Nav: "run", Body: b.Bytes()}
	for _, o := range m.Overviews {
		q := url.Values{"all": {"1"}}
		kind := domain.SubjectAll
		label := "every repository"
		switch o.Scope.Kind {
		case domain.SubjectGroup:
			q, kind, label = url.Values{"group": {o.Scope.Key}}, domain.SubjectGroup, "group "+o.Scope.Key
		case domain.SubjectAll:
		default:
			continue
		}
		if links := s.trace(run.ID, kind, o.Provenance.Metrics, q); len(links) > 0 {
			p.Traces = append(p.Traces, traceGroup{Label: label, Links: links})
		}
	}
	// A repository no overview's table lists (the run's one, or one named
	// outside the run's groups) is listed here, so each has a link.
	for _, repo := range m.Repos {
		if !seen[repo.Key] {
			p.Repos = append(p.Repos, traceLink{Metric: repoLabel(repo),
				URL: s.runPath(run.ID, "repo") + "?" + url.Values{"key": {repo.Key}}.Encode()})
		}
	}
	return p, nil
}

func repoLabel(r report.Repo) string {
	if r.Group != "" {
		return r.Group + "/" + r.Name
	}
	return r.Name
}

func (s *Server) ranksPage(r *http.Request) (*page, error) {
	run, err := s.runID(r)
	if err != nil {
		return nil, err
	}
	m, err := s.model(r.Context(), run.ID)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := report.MarkdownRanks(&b, m, report.AllRows(), report.Links(s.links(run.ID, nil))); err != nil {
		return nil, err
	}
	return &page{Title: fmt.Sprintf("Ranks · run %d", run.ID), Run: &run, Nav: "ranks", Body: b.Bytes(),
		Note: "Every rank is beta: each table says how far its metric is validated, and none has been checked against " +
			"a lead's labels yet ('stillhere label', then 'stillhere validate'). People whose intervals overlap are not " +
			"ordered. A rank describes one property of a person's changes, never the person."}, nil
}

func (s *Server) repoPage(r *http.Request) (*page, error) {
	run, err := s.runID(r)
	if err != nil {
		return nil, err
	}
	key := r.URL.Query().Get("key")
	m, err := s.model(r.Context(), run.ID)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := report.MarkdownRepo(&b, m, key, report.AllRows(), report.Links(s.links(run.ID, nil))); err != nil {
		if errors.Is(err, report.ErrNoRepo) {
			err = fmt.Errorf("run %d holds no repository keyed %q: its page lists them: %w", run.ID, key, domain.ErrNotFound)
		}
		return nil, err
	}
	i := slices.IndexFunc(m.Repos, func(r report.Repo) bool { return r.Key == key })
	repo := m.Repos[i]
	p := &page{Title: repo.Name + " · run " + strconv.FormatInt(run.ID, 10), Run: &run, Nav: "run", Body: b.Bytes(),
		Ask: &askForm{Run: run.ID, Action: s.runPath(run.ID, "who-knows"), Repo: key, Repos: repoChoices(m)}}
	if links := s.trace(run.ID, domain.SubjectRepo, repo.Card.Provenance.Metrics, url.Values{"repo": {key}}); len(links) > 0 {
		p.Traces = []traceGroup{{Label: repo.Name, Links: links}}
	}
	return p, nil
}

func (s *Server) personPage(r *http.Request) (*page, error) {
	run, err := s.runID(r)
	if err != nil {
		return nil, err
	}
	asked := strings.TrimSpace(r.URL.Query().Get("who"))
	if asked == "" {
		return nil, &domain.UsageError{Msg: "a person's page needs who=: a link from another page, or a display name, " +
			"@login or id (p-…)"}
	}
	me, err := app.Me(r.Context(), s.d, app.MeRequest{RunID: run.ID, Email: s.who(asked)})
	if err != nil {
		return nil, err
	}
	h := s.handle(me.Person.Key)
	if asked != h {
		return &page{Redirect: s.runPath(run.ID, "person") + "?" + url.Values{"who": {h}}.Encode()}, nil
	}
	body, err := s.markdown(run.ID, me)
	if err != nil {
		return nil, err
	}
	var held []string
	for _, v := range me.Values {
		if !slices.Contains(held, v.Metric) {
			held = append(held, v.Metric)
		}
	}
	p := &page{Title: "Person · run " + strconv.FormatInt(run.ID, 10), Run: &run, Nav: "run", Body: body,
		Note: "This is the page 'stillhere me' prints for this person: what their lead sees about them, and their own " +
			"AI-assisted share, which no other page shows."}
	if links := s.trace(run.ID, domain.SubjectPerson, held, url.Values{"person": {h}}); len(links) > 0 {
		p.Traces = []traceGroup{{Label: "this person", Links: links}}
	}
	return p, nil
}

func (s *Server) explainPage(r *http.Request) (*page, error) {
	run, err := s.runID(r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	id, err := domain.ParseMetricID(q.Get("metric"))
	if err != nil {
		return nil, err
	}
	asked := strings.TrimSpace(q.Get("person"))
	req := app.ExplainRequest{RunID: run.ID, Metric: id, Repo: q.Get("repo"), Group: q.Get("group"),
		Area: q.Get("area"), AllRepos: q.Get("all") == "1"}
	if asked != "" {
		req.Person = s.who(asked)
	}
	named := 0
	for _, v := range []string{req.Person, req.Repo, req.Group, req.Area} {
		if strings.TrimSpace(v) != "" {
			named++
		}
	}
	if req.AllRepos {
		named++
	}
	if named != 1 {
		return nil, &domain.UsageError{Msg: "explain answers about one subject: person=, repo=, group=, area= or all=1"}
	}
	if req.Days, err = days(q); err != nil {
		return nil, err
	}
	answer, err := app.Explain(r.Context(), s.d, req)
	if err != nil {
		return nil, err
	}
	if asked != "" {
		if h := s.handle(answer.Subject.Key); asked != h {
			q.Set("person", h)
			return &page{Redirect: s.runPath(run.ID, "explain") + "?" + q.Encode()}, nil
		}
	}
	body, err := s.markdown(run.ID, answer)
	if err != nil {
		return nil, err
	}
	return &page{Title: id.String() + " explained · run " + strconv.FormatInt(run.ID, 10), Run: &run, Nav: "run",
		Body: body}, nil
}

func (s *Server) whoKnowsPage(r *http.Request) (*page, error) {
	run, err := s.runID(r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	m, err := s.model(r.Context(), run.ID)
	if err != nil {
		return nil, err
	}
	p := &page{Title: "Who knows · run " + strconv.FormatInt(run.ID, 10), Run: &run, Nav: "who-knows",
		Ask: &askForm{Run: run.ID, Action: s.runPath(run.ID, "who-knows"), Path: q.Get("path"), Repo: q.Get("repo"),
			Days: q.Get("days"), Repos: repoChoices(m)}}
	if strings.TrimSpace(q.Get("path")) == "" {
		p.Note = "Name a path inside a repository, such as src or src/pay.go, to see who knows the code under it: " +
			"majority owners, the top authors of its surviving lines, and its recent landers and reviewers."
		return p, nil
	}
	req := app.WhoKnowsRequest{RunID: run.ID, Path: q.Get("path"), Repo: q.Get("repo")}
	if req.Days, err = days(q); err != nil {
		return nil, err
	}
	answer, err := app.WhoKnows(r.Context(), s.d, req)
	if err != nil {
		return nil, err
	}
	if p.Body, err = s.markdown(run.ID, answer); err != nil {
		return nil, err
	}
	return p, nil
}

// days reads days=, the window explain and who-knows take: 0 when absent.
func days(q url.Values) (int, error) {
	v := strings.TrimSpace(q.Get("days"))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, &domain.UsageError{Msg: fmt.Sprintf("days=%s: want a whole number of days, at least 1", v)}
	}
	return n, nil
}

// repoChoices lists the run's repositories for the who-knows form.
func repoChoices(m *report.Model) []repoChoice {
	out := make([]repoChoice, 0, len(m.Repos))
	for _, r := range m.Repos {
		out = append(out, repoChoice{Key: r.Key, Label: repoLabel(r)})
	}
	return out
}

func (s *Server) runsPage(r *http.Request) (*page, error) {
	runs, err := app.StoredRuns(r.Context(), s.d)
	if err != nil {
		return nil, err
	}
	p := &page{Title: "Runs", Nav: "runs"}
	for _, sr := range runs {
		row := runRow{ID: sr.Run.ID, URL: s.runPath(sr.Run.ID, ""), Status: string(sr.Run.Status), Now: sr.Run.Now,
			Repos: len(sr.Repos)}
		for _, g := range sr.Groups {
			row.Groups = append(row.Groups, g.Name)
		}
		p.Runs = append(p.Runs, row)
	}
	if len(p.Runs) == 0 {
		p.Note = "facts.db holds no complete or partial scan yet: run 'stillhere scan PATH'."
	}
	return p, nil
}

func (s *Server) metricsPage(*http.Request) (*page, error) {
	var b bytes.Buffer
	if err := report.Markdown(&b, app.Metrics(), report.AllRows()); err != nil {
		return nil, err
	}
	return &page{Title: "Metrics", Nav: "metrics", Body: b.Bytes()}, nil
}

// errorPage is the page for a use case's error: its message, which names
// what to change, with 404 for what facts.db does not hold and 400 for a
// request a reader can correct.
func errorPage(err error) *page {
	status := http.StatusInternalServerError
	var usage *domain.UsageError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		status = http.StatusNotFound
	case errors.As(err, &usage):
		status = http.StatusBadRequest
	}
	return &page{Title: "Not answered", Note: err.Error(), Status: status}
}
