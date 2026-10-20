package web

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/csv"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

//go:embed assets
var assets embed.FS

// static is what /static/ serves: the style sheet and the table script.
var static = mustSub(assets, "assets/static")

var layout = template.Must(template.New("layout.html").Funcs(template.FuncMap{
	"date": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
}).ParseFS(assets, "assets/layout.html"))

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// page is what a handler answers with, which the layout renders.
type page struct {
	Title    string
	Run      *domain.Run // the run the page reads; nil for a page about no run
	Nav      string      // the menu item the page sits under: run, ranks, who-knows, metrics or runs
	Note     string      // what to read first: what the page is, or the error that stopped it
	Status   int         // the HTTP status; 0 for 200
	Redirect string      // the address the page is at instead (a person named otherwise than by their handle)
	// Body is the page's Markdown, as the report package renders it.
	Body   []byte
	Traces []traceGroup // links to explain the numbers of the page's subjects
	Repos  []traceLink  // the run's repositories no table of the page links, each to its page
	Ask    *askForm     // the who-knows form
	Runs   []runRow     // the runs page's rows
}

// traceGroup links the numbers about one subject to explain's pages.
type traceGroup struct {
	Label string
	Links []traceLink
}

type traceLink struct{ Metric, URL string }

// askForm is the who-knows form, filled with what was asked.
type askForm struct {
	Run              int64
	Action           string // the who-knows page's address
	Path, Repo, Days string
	Repos            []repoChoice
}

type repoChoice struct{ Key, Label string }

// runRow is one stored scan on the runs page.
type runRow struct {
	ID     int64
	URL    string
	Status string
	Now    time.Time
	Repos  int
	Groups []string
}

// view is what the layout reads: the page, its body as HTML, the server's
// base address and the version.
type view struct {
	*page
	HTML    template.HTML
	Base    string
	Version string
}

// wordJoiner is what the report puts after @ and # (report.mdText), so
// GitHub links no mention or reference in a pasted page. goldmark links
// neither, and a joiner left in would keep a copied "@login" or
// "reviews_given@1" from matching what the command line takes, and the
// table filter from finding it.
var wordJoiner = []byte("\u2060")

// serve serves the page f answers with, one page computing at a time
// (Server.slot): as HTML, or, with csv=N, the page's Nth table as CSV.
func (s *Server) serve(f func(*http.Request) (*page, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, ok := s.hold(r.Context())
		if !ok {
			return // the reader went away while the page waited
		}
		p, err := func() (*page, error) {
			defer release() // a page that panics gives the slot back
			return f(r)
		}()
		if err != nil {
			if r.Context().Err() != nil {
				return // the reader went away
			}
			s.log.Warn("page not answered", "path", r.URL.Path, "error", err)
			p = errorPage(err)
		}
		if p.Redirect != "" {
			http.Redirect(w, r, p.Redirect, http.StatusSeeOther)
			return
		}
		p.Body = bytes.ReplaceAll(p.Body, wordJoiner, nil)
		if n := r.URL.Query().Get("csv"); n != "" && err == nil {
			s.csv(w, p, n)
			return
		}
		s.write(w, r, p)
	})
}

// write renders p as HTML.
func (s *Server) write(w http.ResponseWriter, r *http.Request, p *page) {
	body, err := toHTML(p.Body, r.URL.Query())
	if err != nil {
		s.log.Warn("page not rendered", "path", r.URL.Path, "error", err)
		p, body = errorPage(err), ""
	}
	var b bytes.Buffer
	if err := layout.Execute(&b, view{page: p, HTML: body, Base: s.base, Version: s.version}); err != nil {
		s.log.Warn("page not rendered", "path", r.URL.Path, "error", err)
		http.Error(w, "the page could not be rendered: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if p.Status != 0 {
		w.WriteHeader(p.Status)
	}
	_, _ = b.WriteTo(w)
}

// markdownHTML turns a page's Markdown into HTML: GitHub-flavoured, with
// raw HTML left out and links to dangerous schemes dropped (goldmark's
// defaults), so a name in the facts renders as text, as the report's
// escaping already makes it (report.mdText). Each table is put in a box
// with a link to its CSV (tableBox).
func markdownHTML(csvURL func(n int) string) goldmark.Markdown {
	return goldmark.New(
		// Alignment as an attribute the style sheet reads: the policy
		// (csp) refuses inline style.
		goldmark.WithExtensions(extension.NewTable(
			extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute))),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(boxTables{}, 100))),
		goldmark.WithRendererOptions(
			renderer.WithNodeRenderers(util.Prioritized(boxRenderer{csvURL: csvURL}, 100))),
	)
}

// toHTML renders md as the body of the page whose query is q. The result
// is safe to put in the layout unescaped: goldmark escapes every character
// of text and leaves raw HTML out, and the only markup it is given is the
// report's.
func toHTML(md []byte, q url.Values) (template.HTML, error) {
	if len(md) == 0 {
		return "", nil
	}
	// A query alone: relative to the page's own address, token included.
	csvURL := func(n int) string {
		c := url.Values{}
		for k, vs := range q {
			c[k] = vs
		}
		c.Set("csv", strconv.Itoa(n))
		return "?" + c.Encode()
	}
	var b bytes.Buffer
	if err := markdownHTML(csvURL).Convert(md, &b); err != nil {
		return "", fmt.Errorf("rendering the page: %w", err)
	}
	return template.HTML(b.String()), nil //nolint:gosec // goldmark escaped every character of text and omitted raw HTML (toHTML)
}

// kindTableBox is the kind of tableBox nodes.
var kindTableBox = ast.NewNodeKind("TableBox")

// tableBox holds one of a page's tables, the nth from the top, so it
// renders with a link to its CSV.
type tableBox struct {
	ast.BaseBlock
	n int
}

func (b *tableBox) Kind() ast.NodeKind { return kindTableBox }

func (b *tableBox) Dump(source []byte, level int) { ast.DumpHelper(b, source, level, nil, nil) }

// boxTables puts each table of a document in a tableBox, numbered from 1
// in document order.
type boxTables struct{}

func (boxTables) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	for i, t := range tables(doc) {
		box := &tableBox{n: i + 1}
		parent := t.Parent()
		parent.ReplaceChild(parent, t, box)
		box.AppendChild(box, t)
	}
}

// tables are a document's tables, in document order.
func tables(doc ast.Node) []ast.Node {
	var out []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && n.Kind() == east.KindTable {
			out = append(out, n)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return out
}

// boxRenderer renders a tableBox: the table with a link to its CSV above
// it.
type boxRenderer struct{ csvURL func(n int) string }

func (r boxRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindTableBox, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = fmt.Fprintf(w, `<div class="table"><p class="tools"><a href="%s">CSV</a></p>`+"\n",
				html.EscapeString(r.csvURL(n.(*tableBox).n)))
		} else {
			_, _ = w.WriteString("</div>\n")
		}
		return ast.WalkContinue, nil
	})
}

// csv writes the page's table numbered n (csv=N) as CSV, its header first,
// each cell as the page shows it.
func (s *Server) csv(w http.ResponseWriter, p *page, n string) {
	i, err := strconv.Atoi(n)
	src := p.Body
	doc := markdownHTML(nil).Parser().Parse(text.NewReader(src))
	list := tables(doc)
	if err != nil || i < 1 || i > len(list) {
		http.Error(w, fmt.Sprintf("csv=%s: this page has %d tables, numbered from 1", n, len(list)), http.StatusNotFound)
		return
	}
	var b bytes.Buffer
	out := csv.NewWriter(&b)
	for row := list[i-1].FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, cellText(cell, src))
		}
		if err := out.Write(cells); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	out.Flush()
	name := fmt.Sprintf("stillhere-%s-%d.csv", slug(p.Title), i)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = b.WriteTo(w)
}

// cellText is a table cell's text as the page shows it: its text and code
// spans, their escapes and character references resolved, without the
// word joiners the report puts after @ and # (report.mdText), and with a
// formula's first character quoted, as a spreadsheet would otherwise run
// a name that starts with = (OWASP, "CSV Injection").
func cellText(cell ast.Node, src []byte) string {
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	_ = ast.Walk(cell, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Text:
			if _, code := t.Parent().(*ast.CodeSpan); code {
				_, _ = w.WriteString(html.EscapeString(string(t.Segment.Value(src))))
			} else {
				ghtml.DefaultWriter.Write(w, t.Segment.Value(src))
			}
			if t.SoftLineBreak() || t.HardLineBreak() {
				_ = w.WriteByte(' ')
			}
		case *ast.String:
			_, _ = w.WriteString(html.EscapeString(string(t.Value)))
		}
		return ast.WalkContinue, nil
	})
	_ = w.Flush()
	s := strings.TrimSpace(strings.ReplaceAll(html.UnescapeString(b.String()), "\u2060", ""))
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) && !numberRE.MatchString(s) {
		s = "'" + s
	}
	return s
}

// numberRE matches a negative number as a page prints one, such as -3,
// -1,204 or -12.5%: a cell a spreadsheet takes as that number, which is no
// formula and stays as it is. Any other cell that starts with a formula's
// character is quoted, an exponent or a sign of + included.
var numberRE = regexp.MustCompile(`^-\d[\d,]*(\.\d+)?%?$`)

// slug is a page title as a file name: lower-case letters, digits and
// dashes.
func slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
