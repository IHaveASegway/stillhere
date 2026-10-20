package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark/text"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/fixture"
	"github.com/IHaveASegway/stillhere/internal/gitcli"
	"github.com/IHaveASegway/stillhere/internal/policy"
	"github.com/IHaveASegway/stillhere/internal/report"
	"github.com/IHaveASegway/stillhere/internal/sqlite"
)

// The hostile repository's untrusted text: a directory, an author's name
// and a commit subject that would be markup, a formula or a script link if
// a page or a CSV passed them through.
const (
	hostileDir     = `<img src=x onerror=alert(1)>`
	hostileName    = `=HYPERLINK("https://example.invalid")`
	hostileEmail   = "formula@example.com"
	hostileSubject = `<script>alert(1)</script> [x](javascript:alert(1))`
)

// TestWebServer drives the server over HTTP, as a browser does, against a
// scan of the platform fixture and a hostile repository stored through the
// real adapters: the token and the Host check, the pages' links and their
// numbers, which are the command line's, the CSV of every table, and the
// escaping of what the facts hold.
func TestWebServer(t *testing.T) {
	ctx := t.Context()
	db, mailmap, platform := scanPlatform(t)
	const token = "test-token"
	srv, ln := start(t, db, token)
	host := ln.Addr().String()
	root := "http://" + host
	base := root + "/" + token + "/"
	if srv.URL() != base {
		t.Fatalf("URL() = %q, want %q", srv.URL(), base)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	follow := &http.Client{}

	t.Run("addresses", func(t *testing.T) {
		for _, ok := range []string{"127.0.0.1:0", "127.0.0.1:8080", "[::1]:0", "localhost:0"} {
			if err := CheckAddr(ok); err != nil {
				t.Errorf("CheckAddr(%q) = %v, want nil", ok, err)
			}
		}
		for _, bad := range []string{"0.0.0.0:8080", ":8080", "192.168.1.2:80", "example.com:80", "[::]:80", "127.0.0.1",
			"127.0.0.1:http", "127.0.0.1:70000"} {
			if err := CheckAddr(bad); err == nil {
				t.Errorf("CheckAddr(%q) = nil, want an error", bad)
			}
		}
	})

	t.Run("token", func(t *testing.T) {
		// Every address starts with the token, and nothing else opens a
		// page: no cookie, which every port of the host would be sent.
		for _, u := range []string{root + "/", root + "/run/1/", root + "/static/style.css", root + "/test-tokenx/run/1/",
			root + "/wrong/run/1/", root + "/?token=" + token} {
			if code := status(t, c, u, nil); code != http.StatusForbidden {
				t.Errorf("GET %s: %d, want 403", u, code)
			}
		}
		if res := get(t, c, root+"/"+token, nil); res.Code != http.StatusSeeOther || res.Header.Get("Location") != "/"+token+"/" {
			t.Errorf("the token alone: %d to %q, want 303 to the base", res.Code, res.Header.Get("Location"))
		}
		for _, u := range []string{base, base + "run/1/", base + "static/style.css", base + "runs", base + "metrics"} {
			res := get(t, c, u, nil)
			if res.Code != http.StatusOK && res.Code != http.StatusSeeOther {
				t.Errorf("GET %s: %d", u, res.Code)
			}
			if len(res.Cookies) > 0 || res.Header.Get("Set-Cookie") != "" {
				t.Errorf("GET %s sets a cookie", u)
			}
		}
	})

	t.Run("host", func(t *testing.T) {
		// A name that resolves here but is not the address the server
		// printed: DNS rebinding, or localhost, which may reach the other
		// address family. Its pages are not served under it.
		for _, h := range []string{"evil.example", "evil.example:" + port(host), "127.0.0.1", "localhost:" + port(host),
			"127.0.0.1:1"} {
			if code := status(t, c, base+"run/1/", map[string]string{"Host": h}); code != http.StatusMisdirectedRequest {
				t.Errorf("Host %q: %d, want 421", h, code)
			}
		}
	})

	t.Run("headers and methods", func(t *testing.T) {
		res := get(t, c, base+"run/1/", nil)
		for k, want := range map[string]string{
			"Content-Security-Policy": csp, "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store",
			"Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY", "Content-Type": "text/html; charset=utf-8",
		} {
			if got := res.Header.Get(k); got != want {
				t.Errorf("%s: %q, want %q", k, got, want)
			}
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			req, err := http.NewRequestWithContext(ctx, method, base+"run/1/", strings.NewReader("x"))
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s: %d, want 405", method, res.StatusCode)
			}
		}
	})

	// Every page a link reaches from the base.
	pages := crawl(t, follow, root, "/"+token+"/")

	t.Run("pages", func(t *testing.T) {
		emails := runEmails(t, db)
		emails = append(emails, hostileEmail)
		kinds := map[string]int{}
		for u, body := range pages {
			kinds[strings.SplitN(strings.TrimPrefix(u, "/"+token), "?", 2)[0]]++
			lower := strings.ToLower(body)
			for _, e := range emails {
				if strings.Contains(lower, strings.ToLower(e)) {
					t.Errorf("%s holds the email %s", u, e)
				}
			}
			// The one script is the server's own; nothing the facts hold
			// becomes markup or a script link.
			if n := strings.Count(lower, "<script"); n != 1 {
				t.Errorf("%s holds %d script elements, want the page's own one", u, n)
			}
			for _, tag := range tagRE.FindAllString(lower, -1) {
				bare := quotedRE.ReplaceAllString(tag, `""`)
				if strings.HasPrefix(tag, "<img") || handlerRE.MatchString(bare) {
					t.Errorf("%s holds the markup %s", u, tag)
				}
			}
			for _, h := range hrefs(body) {
				if !strings.HasPrefix(h, "/"+token+"/") && !strings.HasPrefix(h, "?") {
					t.Errorf("%s links to %s, outside the server's base", u, h)
				}
				if strings.Contains(h, "/person?") {
					who := mustParse(t, h).Query().Get("who")
					if !handleRE.MatchString(who) {
						t.Errorf("%s links a person by %q, want their handle", u, who)
					}
				}
			}
			// A trace link leads to a value, never to "No value".
			if strings.Contains(u, "/explain?") && strings.Contains(body, "No value in this run") {
				t.Errorf("%s was linked but holds no value", u)
			}
		}
		for _, kind := range []string{"/", "/runs", "/metrics", "/run/1/", "/run/1/ranks", "/run/1/repo", "/run/1/person",
			"/run/1/explain", "/run/1/who-knows"} {
			if kinds[kind] == 0 {
				t.Errorf("no page %s reached from the base", kind)
			}
		}
		// Each person link opens that person's page, which heads itself with
		// the name the link showed.
		for u, body := range pages {
			for _, m := range personLinkRE.FindAllStringSubmatch(body, -1) {
				target, name := html.UnescapeString(m[1]), html.UnescapeString(m[2])
				if strings.HasPrefix(name, "(") {
					continue // a label counts per page
				}
				page, ok := pages[target]
				if !ok {
					page = get(t, follow, root+target, nil).Body
				}
				if h1 := h1RE.FindString(page); !strings.Contains(html.UnescapeString(h1), name) {
					t.Errorf("%s links %q to %s, whose heading is %s", u, name, target, h1)
				}
			}
		}
		// The hostile directory is an area, linked to who knows it, and
		// shown as the text it is, as the hostile subject is.
		if !slices.ContainsFunc(keys(pages), func(u string) bool {
			return strings.Contains(u, "/who-knows?") && mustParse(t, u).Query().Get("path") == hostileDir+"/"
		}) {
			t.Error("no who-knows page of the hostile area was linked")
		}
		for _, text := range []string{hostileDir, "<script>alert(1)</script>"} {
			var shown bool
			for _, body := range pages {
				shown = shown || strings.Contains(body, html.EscapeString(text))
			}
			if !shown {
				t.Errorf("no page shows %s as text", text)
			}
		}
	})

	t.Run("people named otherwise", func(t *testing.T) {
		// A person asked about by email, name or id is sent to the same
		// page by their handle, so the address leaves the address bar.
		email := runEmails(t, db)[0]
		for _, u := range []string{
			base + "run/1/person?" + url.Values{"who": {email}}.Encode(),
			base + "run/1/explain?" + url.Values{"metric": {"landed@2"}, "person": {email}}.Encode(),
		} {
			res := get(t, c, u, nil)
			to := res.Header.Get("Location")
			if res.Code != http.StatusSeeOther || strings.Contains(strings.ToLower(to), strings.ToLower(email)) ||
				!handleRE.MatchString(mustParse(t, to).Query().Get(map[bool]string{true: "who", false: "person"}[strings.Contains(u, "/person?")])) {
				t.Errorf("GET %s: %d to %q, want 303 to the page by handle", u, res.Code, to)
			}
		}
	})

	t.Run("same numbers", func(t *testing.T) {
		// The run's report as the command line prints it, with every rank
		// table the ranks page shows, and its tables, against the pages'
		// tables in the order the report prints them: the run's page, its
		// ranks, then each repository's page. Labels of people a page
		// cannot name count per page, so they are compared without their
		// number.
		st, err := sqlite.OpenReadOnly(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		cli, err := app.Report(ctx, app.Deps{Store: st, ForgeStore: st}, app.ReportRequest{RunID: 1, Rank: app.RankedMetrics()})
		if err != nil {
			t.Fatal(err)
		}
		var md bytes.Buffer
		if err := report.Markdown(&md, cli, report.AllRows()); err != nil {
			t.Fatal(err)
		}
		want := markdownTables(md.Bytes())
		var got [][][]string
		order := []string{base + "run/1/", base + "run/1/ranks"}
		for _, r := range cli.Repos {
			order = append(order, base+"run/1/repo?"+url.Values{"key": {r.Key}}.Encode())
		}
		for _, u := range order {
			got = append(got, pageTables(t, c, u)...)
		}
		if len(got) != len(want) || len(want) < 10 {
			t.Fatalf("the pages hold %d tables, the report %d, want the same, at least 10", len(got), len(want))
		}
		t.Logf("%d tables, %d pages", len(want), len(pages))
		label := regexp.MustCompile(`\((name withheld|no name) \d+\)`)
		for i := range want {
			g, w := label.ReplaceAllString(flat(got[i]), "($1)"), label.ReplaceAllString(flat(want[i]), "($1)")
			if g != w {
				t.Errorf("table %d:\n got %s\nwant %s", i+1, g, w)
			}
		}
	})

	t.Run("csv", func(t *testing.T) {
		res := get(t, c, base+"run/1/?csv=1", nil)
		if res.Code != http.StatusOK || res.Header.Get("Content-Type") != "text/csv; charset=utf-8" ||
			!strings.HasPrefix(res.Header.Get("Content-Disposition"), `attachment; filename="stillhere-run-1-1.csv"`) {
			t.Errorf("csv=1: %d %q %q", res.Code, res.Header.Get("Content-Type"), res.Header.Get("Content-Disposition"))
		}
		if _, err := csv.NewReader(strings.NewReader(res.Body)).ReadAll(); err != nil {
			t.Errorf("csv=1 is not CSV: %v", err)
		}
		for _, n := range []string{"0", "99", "x"} {
			if code := status(t, c, base+"run/1/?csv="+n, nil); code != http.StatusNotFound {
				t.Errorf("csv=%s: %d, want 404", n, code)
			}
		}
		// A name a spreadsheet would run as a formula is quoted.
		var found bool
		for u := range pages {
			for _, tbl := range pageTables(t, follow, root+u) {
				for _, row := range tbl {
					for _, cell := range row {
						if strings.Contains(cell, "HYPERLINK") {
							found = true
							if !strings.HasPrefix(cell, "'=") {
								t.Errorf("%s: cell %q, want it quoted as text", u, cell)
							}
						}
					}
				}
			}
		}
		if !found {
			t.Error("no table names the hostile author")
		}
	})

	t.Run("errors", func(t *testing.T) {
		for u, want := range map[string]int{
			"run/99/":                              http.StatusNotFound,
			"run/x/":                               http.StatusBadRequest,
			"run/1/repo?key=nope":                  http.StatusNotFound,
			"run/1/explain?metric=durable@1":       http.StatusBadRequest,
			"run/1/explain?metric=nope@1&repo=x":   http.StatusBadRequest,
			"run/1/person?who=":                    http.StatusBadRequest,
			"run/1/person?who=p-0000000000":        http.StatusNotFound,
			"run/1/who-knows?path=src&days=0":      http.StatusBadRequest,
			"run/1/explain?metric=durable@1&all=1": http.StatusBadRequest,
			"nope":                                 http.StatusNotFound,
			"run/1/who-knows?" + url.Values{"path": {hostileDir + "/"}}.Encode(): http.StatusOK,
		} {
			res := get(t, c, base+u, nil)
			if res.Code != want {
				t.Errorf("GET %s: %d, want %d: %s", u, res.Code, want, noteOf(res.Body))
			}
		}
	})

	t.Run("a new run", func(t *testing.T) {
		// A scan published while the server runs is the next one the base
		// sends the reader to.
		scan(t, db, mailmap, platform)
		res := get(t, c, base, nil)
		if res.Code != http.StatusSeeOther || res.Header.Get("Location") != "/"+token+"/run/2/" {
			t.Errorf("the base after a second scan: %d to %q, want 303 to run 2", res.Code, res.Header.Get("Location"))
		}
		if body := get(t, c, base+"runs", nil).Body; !strings.Contains(body, `href="/`+token+`/run/2/"`) ||
			!strings.Contains(body, `href="/`+token+`/run/1/"`) {
			t.Errorf("the runs page does not link both runs")
		}
	})

	t.Run("one repository", func(t *testing.T) {
		// A run of one repository has no overview of its own (its card is
		// it), and the run's page still links the repository's page.
		dir := filepath.Join(t.TempDir(), "solo")
		if err := fixture.BuildContext(ctx, dir); err != nil {
			t.Fatal(err)
		}
		soloDB := filepath.Join(t.TempDir(), "facts.db")
		scan(t, soloDB, mailmap, dir)
		_, soloLn := start(t, soloDB, token)
		soloRoot := "http://" + soloLn.Addr().String()
		reached := crawl(t, follow, soloRoot, "/"+token+"/")
		var repo bool
		for u := range reached {
			repo = repo || strings.Contains(u, "/run/1/repo?")
		}
		if !repo {
			t.Errorf("no repository page is reached from a run of one repository (%d pages)", len(reached))
		}
	})
}

// start serves db on a new loopback listener with token until the test
// ends, and returns the server and its listener.
func start(t *testing.T, db, token string) (*Server, net.Listener) {
	t.Helper()
	st, err := sqlite.OpenReadOnly(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{Store: st, Token: token, Version: "test"}, ln)
	if err != nil {
		t.Fatal(err)
	}
	serving, stop := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serving) }()
	t.Cleanup(func() {
		stop()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return srv, ln
}

// crawl visits every page of the server under base that a link reaches
// from base, CSVs aside, following redirects, and returns each page's body
// by its path and query. It fails the test rather than stop short.
func crawl(t *testing.T, c *http.Client, root, base string) map[string]string {
	t.Helper()
	const most = 2000
	pages := map[string]string{}
	queue := []string{base}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		if _, ok := pages[u]; ok {
			continue
		}
		if len(pages) == most {
			t.Fatalf("the crawl reached %d pages and stopped short", most)
		}
		res := get(t, c, root+u, nil)
		if res.Code != http.StatusOK {
			t.Errorf("GET %s: %d: %s", u, res.Code, noteOf(res.Body))
		}
		pages[u] = res.Body
		for _, h := range hrefs(res.Body) {
			if strings.HasPrefix(h, base) && !strings.Contains(h, "csv=") && !strings.Contains(h, "/static/") {
				queue = append(queue, h)
			}
		}
	}
	return pages
}

// handleRE matches a person's handle on the server's pages (Server.handle).
var handleRE = regexp.MustCompile(`^u-[0-9a-f]{16}$`)

// personLinkRE matches a link to a person's page and its text, and h1RE a
// page's heading.
var (
	personLinkRE = regexp.MustCompile(`<a href="([^"]*/person\?who=[^"]*)">([^<]*)</a>`)
	h1RE         = regexp.MustCompile(`(?s)<h1[^>]*>.*?</h1>`)
)

var hrefRE = regexp.MustCompile(`href="([^"]*)"`)

// tagRE matches an element's start tag: what the text of a page cannot
// make, since it escapes every <.
var tagRE = regexp.MustCompile(`<[a-z][^>]*>`)

// quotedRE matches an attribute's quoted value, and handlerRE an event
// handler attribute once the values are gone.
var (
	quotedRE  = regexp.MustCompile(`"[^"]*"`)
	handlerRE = regexp.MustCompile(`\son[a-z]+\s*=`)
)

func hrefs(body string) []string {
	var out []string
	for _, m := range hrefRE.FindAllStringSubmatch(body, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// pageTables are a page's tables, as its CSVs give them.
func pageTables(t *testing.T, c *http.Client, u string) [][][]string {
	t.Helper()
	var out [][][]string
	for n := 1; ; n++ {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		res := get(t, c, u+sep+"csv="+strconv.Itoa(n), nil)
		body := res.Body
		switch res.Code {
		case http.StatusOK:
		case http.StatusNotFound:
			return out
		default:
			t.Fatalf("%s csv=%d: %d: %s", u, n, res.Code, firstLine(body))
		}
		rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
		if err != nil {
			t.Fatalf("%s csv=%d: %v", u, n, err)
		}
		out = append(out, rows)
	}
}

// markdownTables are the tables of a Markdown page, each cell's text as a
// page's CSV gives it.
func markdownTables(md []byte) [][][]string {
	doc := markdownHTML(nil).Parser().Parse(text.NewReader(md))
	var out [][][]string
	for _, tbl := range tables(doc) {
		var rows [][]string
		for row := tbl.FirstChild(); row != nil; row = row.NextSibling() {
			var cells []string
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				cells = append(cells, cellText(cell, md))
			}
			rows = append(rows, cells)
		}
		out = append(out, rows)
	}
	return out
}

func flat(rows [][]string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(strings.Join(r, " | "))
		b.WriteString(" / ")
	}
	return b.String()
}

// reply is a response, read whole.
type reply struct {
	Code    int
	Header  http.Header
	Cookies []*http.Cookie
	Body    string
}

// get sends a GET of u with headers (Host among them) and reads the reply.
func get(t *testing.T, c *http.Client, u string, headers map[string]string) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{Code: res.StatusCode, Header: res.Header, Cookies: res.Cookies(), Body: string(b)}
}

func status(t *testing.T, c *http.Client, u string, headers map[string]string) int {
	t.Helper()
	return get(t, c, u, headers).Code
}

func port(addr string) string {
	_, p, _ := strings.Cut(addr, ":")
	return p
}

func mustParse(t *testing.T, u string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var noteRE = regexp.MustCompile(`<p class="note">([^<]*)</p>`)

func noteOf(body string) string {
	if m := noteRE.FindStringSubmatch(body); m != nil {
		return html.UnescapeString(m[1])
	}
	return firstLine(body)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// scanPlatform builds the platform fixture, the fixture and its sibling,
// with the hostile repository beside them, and stores one scan of it in a
// new facts.db through the adapters internal/cli builds.
func scanPlatform(t *testing.T) (db, mailmap, platform string) {
	t.Helper()
	ctx := t.Context()
	root := t.TempDir()
	platform = filepath.Join(root, "platform")
	if err := fixture.BuildContext(ctx, filepath.Join(platform, "fixture")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.BuildSiblingContext(ctx, filepath.Join(platform, "sibling")); err != nil {
		t.Fatal(err)
	}
	day := fixture.Now.AddDate(0, 0, -3).Format(time.DateOnly)
	buildRepo(t, filepath.Join(platform, "hostile"), []testCommit{
		{hostileName, hostileEmail, day, hostileSubject, hostileDir + "/pay.go", "package pay\n\nvar a = 1\nvar b = 2\n"},
	})
	mailmap = filepath.Join(root, "mailmap")
	if err := os.WriteFile(mailmap, []byte(fixture.MailmapText+fixture.SiblingMailmapText), 0o600); err != nil {
		t.Fatal(err)
	}
	db = filepath.Join(root, "facts.db")
	scan(t, db, mailmap, platform)
	return db, mailmap, platform
}

// scan stores one scan of the group at path in db, as of fixture.Now,
// ranking nothing: the pages rank every ranked metric themselves.
func scan(t *testing.T, db, mailmap, path string) {
	t.Helper()
	ctx := t.Context()
	git, err := gitcli.New(ctx, gitcli.Config{MailmapFile: mailmap})
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Default()
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.Scan(ctx, app.Deps{Git: git, Store: store, ForgeStore: store, Policy: p, Version: "test"},
		app.ScanRequest{Paths: []string{path}, Now: fixture.Now})
	if cerr := store.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
}

// runEmails are the emails of the people of the latest run in db.
func runEmails(t *testing.T, db string) []string {
	t.Helper()
	st, err := sqlite.OpenReadOnly(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	n, err := app.RunNames(t.Context(), app.Deps{Store: st, ForgeStore: st}, app.NamesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range n.People {
		if strings.Contains(p.Key, "@") && !strings.HasPrefix(p.Key, "@") {
			out = append(out, p.Key)
		}
	}
	if len(out) == 0 {
		t.Fatal("the run names no email")
	}
	return out
}

// testCommit is one commit of a repository buildRepo makes: one file.
type testCommit struct {
	name, email, day, subject, path, content string
}

// buildRepo makes a git repository in dir from commits, with git run as
// internal/fixture runs it, without the machine's configuration (as the
// MCP server's tests build theirs). Each commit lands at noon UTC on its
// day.
func buildRepo(t *testing.T, dir string, commits []testCommit) {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_ATTR_NOSYSTEM=1")
	git := func(who []string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git",
			append([]string{"-c", "core.excludesFile=/dev/null", "-c", "core.attributesFile=/dev/null"}, args...)...)
		cmd.Dir, cmd.Env = dir, append(slices.Clone(env), who...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git(nil, "init", "--quiet", "--template=", "--initial-branch=main", "--object-format=sha1")
	for _, c := range commits {
		when, err := time.Parse(time.DateOnly, c.day)
		if err != nil {
			t.Fatal(err)
		}
		date := strconv.FormatInt(when.Add(12*time.Hour).Unix(), 10) + " +0000"
		full := filepath.Join(dir, filepath.FromSlash(c.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		git(nil, "add", "--", c.path)
		git([]string{"GIT_AUTHOR_NAME=" + c.name, "GIT_AUTHOR_EMAIL=" + c.email, "GIT_AUTHOR_DATE=" + date,
			"GIT_COMMITTER_NAME=" + c.name, "GIT_COMMITTER_EMAIL=" + c.email, "GIT_COMMITTER_DATE=" + date},
			"commit", "--quiet", "--message", c.subject)
	}
}
