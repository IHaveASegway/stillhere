// Package web is stillhere's web server (M3, #17, ADR 0005): an inbound
// adapter beside the command line and the MCP server. It calls the same
// read use cases and renders their answers with the same report package,
// so a number on a page is the number the command line prints: a page is
// the Markdown the report renders (report.MarkdownRun and the answers'
// Markdown), with each person, repository, area and ranked estimate linked
// to a page of its own (report.Links), turned into HTML by goldmark. A
// definition of how a number reads lives in internal/report only, as the
// definition of the number lives in internal/metrics only.
//
// This is ADR 0005's local mode, M3's first slice: it serves the person who
// scanned, on their own machine, what the command line already shows them
// (docs/product.md, decision 7). It listens on a loopback address only
// (Listen) and answers only requests whose Host is that address, so a page
// elsewhere cannot reach it by DNS rebinding. Every address it serves
// starts with a random token, a capability URL (W3C TAG, "Good Practices
// for Capability URLs"): only who holds the address it printed can read a
// page. No cookie carries the token, since a cookie is sent to every port
// of a host (RFC 6265, section 8.5), so another program listening on
// 127.0.0.1 could take it from the browser: the address is the only
// credential, and no page sends it anywhere (no referrer, no resource
// from elsewhere). Another account on the machine, which cannot read
// facts.db (0600), cannot read its pages either, and the file stays the
// boundary (docs/architecture.md#security-and-privacy). It reads facts.db
// read-only and writes nothing. Sign-in, the leads team, the audit log and
// the AI-assisted shares' floor are the team server's, which is not built.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IHaveASegway/stillhere/internal/app"
	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/report"
)

// Config is what the composition root hands the server.
type Config struct {
	// Store is facts.db, opened read-only. The server reads it through the
	// run cache (app.CacheRuns) and closes it when Serve returns.
	Store domain.FactStore
	// Token is the secret every address of the server starts with; ""
	// makes a new one (tests set it). It must be URL-safe base64.
	Token   string
	Version string       // the tool version, for the pages' footer
	Log     *slog.Logger // nil discards it
}

// Server is the web server over one facts.db, listening on one loopback
// address. It is an http.Handler.
type Server struct {
	d       app.Deps
	ln      net.Listener
	host    string // the Host a request must name: the listener's address
	token   string
	base    string // "/" + token + "/": every page's address starts with it
	version string
	log     *slog.Logger
	inner   http.Handler // the pages, under base

	// slot lets one page compute at a time, as the MCP server runs one
	// tool call at a time: the run cache shares one run's facts between
	// pages, and a page is quick once its run is cached, so serialising
	// them costs one reader little and rules out a data race on facts a
	// use case should never write. A request waits for the slot or for its
	// own context, so one the reader left stops waiting. What the fields
	// below hold is read and written only by the holder of the slot.
	slot   chan struct{}
	models map[int64]*report.Model // each run's report with every ranked metric ranked, built once (model)
	order  []int64                 // the runs of the cached reports, the most recently used last
	// people names each person a page linked by a handle of the server's
	// own (handle): a keyed hash of their key under hkey, which is made
	// anew each time the server starts, so a link holds nothing a reader
	// could test a guessed email against, as the pages withhold a name that
	// gives an address away (report's names.go).
	people map[string]string
	hkey   []byte
}

// CheckAddr returns an error unless addr is HOST:PORT on a loopback
// address: until the team server's sign-in exists, the server serves the
// machine it runs on and nothing else (ADR 0005).
func CheckAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not HOST:PORT, such as 127.0.0.1:8080 (%w)", addr, err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("%q: port %q is not a number from 0 to 65535", addr, port)
	}
	if !loopback(host) {
		return fmt.Errorf("%q is not a loopback address: the server has no sign-in yet, so it serves only the machine "+
			"it runs on (ADR 0005); use 127.0.0.1, ::1 or localhost", addr)
	}
	return nil
}

// loopback reports whether host names this machine only: localhost, or a
// loopback IP address.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Listen listens on addr, which CheckAddr must accept.
func Listen(ctx context.Context, addr string) (net.Listener, error) {
	if err := CheckAddr(addr); err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	return ln, nil
}

// New returns the server for cfg on ln, a loopback listener (Listen): a
// listener on any other address is refused, whatever name resolved to it.
func New(cfg Config, ln net.Listener) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("web: no facts store")
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		return nil, fmt.Errorf("web: %s is not a loopback address", ln.Addr())
	}
	token := cfg.Token
	if token == "" {
		var err error
		if token, err = secret(); err != nil {
			return nil, err
		}
	}
	if strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return nil, errors.New("web: the token must be URL-safe base64")
	}
	hkey := make([]byte, 32)
	if _, err := rand.Read(hkey); err != nil {
		return nil, fmt.Errorf("web: making the people's key: %w", err)
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	store, forge := app.CacheRuns(cfg.Store)
	s := &Server{
		d:  app.Deps{Store: store, ForgeStore: forge, Log: log, Version: cfg.Version},
		ln: ln,
		// The address it prints, and no other name: localhost may resolve
		// to the other address family, which another program can hold on
		// the same port.
		host:    tcp.String(),
		token:   token,
		base:    "/" + token + "/",
		version: cfg.Version,
		log:     log,
		slot:    make(chan struct{}, 1),
		models:  map[int64]*report.Model{},
		people:  map[string]string{},
		hkey:    hkey,
	}
	s.inner = http.StripPrefix("/"+token, s.routes())
	return s, nil
}

// secret is 32 random bytes as URL-safe base64.
func secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("web: making the token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// URL is the address to open: the server's first page, under its token.
func (s *Server) URL() string {
	return "http://" + s.host + s.base
}

// Serve serves until ctx is done, then stops taking requests, lets the
// ones in flight finish for up to five seconds, and closes the store.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A page of a large run computes in a second or two the first time;
		// this bounds one that never ends.
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  2 * time.Minute,
		ErrorLog:     slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		// Requests outlive ctx by Shutdown's grace, not a moment more.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(s.ln) }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = srv.Shutdown(shut)
		<-done
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, s.d.Store.Close())
}

// csp lets a page load its own style sheet and script and nothing else:
// no inline script or style, no frame, no form that sends elsewhere.
const csp = "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self'; form-action 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'"

// ServeHTTP checks the request's Host and token, then serves it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	// The token is in every address: no page may send its address on.
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	// Pages name people: no copy of one is kept on disk.
	h.Set("Cache-Control", "no-store")
	if r.Host != s.host {
		// A name that resolved to this address but is not its own: DNS
		// rebinding, or a proxy. Its pages are not served under it.
		http.Error(w, "stillhere serves only "+s.host, http.StatusMisdirectedRequest)
		return
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if subtle.ConstantTimeCompare([]byte(first), []byte(s.token)) != 1 {
		http.Error(w, "Open the address 'stillhere serve' printed: every page's address starts with its token.",
			http.StatusForbidden)
		return
	}
	if r.URL.Path == "/"+s.token {
		http.Redirect(w, r, s.base, http.StatusSeeOther)
		return
	}
	s.inner.ServeHTTP(w, r)
}

// hold waits for the slot, or for ctx, and returns the function that
// gives the slot back, or false when ctx ended first.
func (s *Server) hold(ctx context.Context) (release func(), ok bool) {
	select {
	case s.slot <- struct{}{}:
		return func() { <-s.slot }, true
	case <-ctx.Done():
		return nil, false
	}
}
