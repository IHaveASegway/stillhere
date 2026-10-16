// Package github is the outbound adapter that implements domain.ForgeSource
// over GitHub: the GraphQL API for repositories, pull requests and reviews,
// and git over HTTPS for the managed mirrors (ADR 0004). It is the only
// package that talks to GitHub, and only stillhere sync runs it.
//
// It is the anti-corruption layer of the Forge context: GitHub's JSON shapes
// stay in this package and only domain types leave it, with GitHub's states
// translated (MERGED becomes domain.PullMerged) and its quirks absorbed (a
// deleted account becomes a ForgeUser without a login; an app becomes a
// Bot).
//
// A Source holds the token and never hands it out. The API gets it in an
// Authorization header from go-gh's client; git gets it only as an
// environment-scoped http.extraheader on the Sync profile
// (docs/spec/git-invocation.md#credentials), so it is never in argv, a URL, a
// config file, a log line, a fact or an error.
//
// GitHub asks API clients to avoid many concurrent requests, to slow down
// before the hourly points run out, and to back off from secondary rate
// limits with growing waits. A Source does all three whatever its caller's
// concurrency: mirrors fetch in parallel, while API requests queue behind a
// gate of three, and a rate limit one of them meets holds them all.
package github

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// DefaultHost is the forge host when Config.Host is empty.
const DefaultHost = "github.com"

// requestTimeout bounds one API request. GitHub ends a GraphQL query that
// runs past 10 seconds itself; the rest is for a large response to arrive.
const requestTimeout = time.Minute

// Config configures a Source.
type Config struct {
	// Host is the GitHub host, "github.com" when empty. go-gh derives the
	// API endpoint from it (api.github.com, or /api/graphql on GitHub
	// Enterprise Server).
	Host string
	// Token authenticates every request. When empty, New looks one up the
	// way gh does: GH_TOKEN, then GITHUB_TOKEN, then gh's stored token.
	Token string
	// Git runs the mirror commands. Mirror fails without it.
	Git domain.GitRunner
	// Logger receives info lines for waits and retries and debug lines for
	// each request. It never receives the token. nil discards them.
	Logger *slog.Logger
	// UserAgent names stillhere to GitHub, such as "stillhere/0.2.0";
	// "stillhere" when empty.
	UserAgent string

	// GitURL is the base URL that mirrors clone from, "https://" + Host when
	// empty. It must be https, except on a loopback host, where a test
	// serves repositories.
	GitURL string
	// Transport carries the API requests, http.DefaultTransport when nil.
	// Tests replay recorded responses through it.
	Transport http.RoundTripper
	// Sleep waits out a rate limit or a backoff. nil waits on a timer that
	// ctx cancels; tests record the waits instead.
	Sleep func(ctx context.Context, d time.Duration) error
	// Clock reads the local clock, for the time since GitHub's last Date
	// and for how long a rate limit holds every request. nil is time.Now;
	// tests pass one that Sleep moves on.
	Clock func() time.Time

	// ChangedPaths says the git binary writes version 2 changed-path Bloom
	// filters (git 2.46 and later): each mirror's commit-graph then gets
	// them, which make blame several times faster (#56). Without, a mirror
	// gets a commit-graph with no filters, because version 1 filters hash
	// paths that are not ASCII differently on some platforms
	// (docs/spec/git-invocation.md#sync-commands-network).
	ChangedPaths bool
}

// Source is the GitHub adapter. It implements domain.ForgeSource and is safe
// for concurrent use.
type Source struct {
	api    *api.GraphQLClient
	seen   *observer     // GitHub's clock, from the Date of the last response
	gate   chan struct{} // holds one token per API request in flight: at most maxInFlight
	mu     sync.Mutex    // guards budget and held
	budget budget        // the rate-limit state GitHub last reported
	held   time.Time     // on clock: until when a rate limit holds every request (hold)

	git          domain.GitRunner
	gitURL       string       // base URL of clone URLs, without a trailing slash
	creds        *credentials // the credential variables of network git commands
	changedPaths bool         // Config.ChangedPaths

	log   *slog.Logger
	sleep func(context.Context, time.Duration) error
	clock func() time.Time
}

var _ domain.ForgeSource = (*Source)(nil)

// New builds a Source. A missing token, or one GitHub's client cannot use,
// is a *domain.UsageError, because the user fixes it by signing in.
//
// The token lookup is go-gh's auth.TokenForHost, so a user's gh auth login
// works unchanged. That function takes no context: when it falls back to gh's
// stored token it runs "gh auth token", which a locked keychain can hold,
// and only a second interrupt ends that wait. ctx is checked before and
// after.
func New(ctx context.Context, cfg Config) (*Source, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	token := cfg.Token
	if token == "" {
		var err error
		if token, err = lookupToken(ctx, host, log); err != nil {
			return nil, err
		}
	}
	gitURL, err := gitBase(cfg.GitURL, host)
	if err != nil {
		return nil, err
	}

	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = "stillhere"
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	seen := &observer{base: transport, clock: clock}
	client, err := api.NewGraphQLClient(api.ClientOptions{
		Host:      host,
		AuthToken: token,
		Transport: seen,
		Timeout:   requestTimeout,
		Headers: map[string]string{
			"User-Agent": userAgent,
			// go-gh sends the machine's time zone otherwise; UTC keeps
			// every request the same on every machine.
			"Time-Zone": "UTC",
		},
		// GH_DEBUG would make go-gh print requests and response bodies,
		// which hold emails, to stderr behind stillhere's logger.
		LogIgnoreEnv: true,
	})
	if err != nil {
		// go-gh's message names the host or its configured api_host, never
		// the token.
		return nil, &domain.UsageError{Msg: fmt.Sprintf("GitHub client for %s: %v", host, err)}
	}

	sleeper := cfg.Sleep
	if sleeper == nil {
		sleeper = sleep
	}
	return &Source{
		api:          client,
		seen:         seen,
		gate:         make(chan struct{}, maxInFlight),
		budget:       budget{remaining: -1},
		git:          cfg.Git,
		gitURL:       gitURL,
		creds:        newCredentials(gitURL, token),
		changedPaths: cfg.ChangedPaths,
		log:          log,
		sleep:        sleeper,
		clock:        clock,
	}, nil
}

// lookupToken finds the token the way gh itself does (go-gh's
// auth.TokenForHost): GH_TOKEN, then GITHUB_TOKEN (GH_ENTERPRISE_TOKEN and
// GITHUB_ENTERPRISE_TOKEN for an Enterprise host), then gh's hosts.yml, then
// the system keyring through "gh auth token". Only the name of the source is
// logged.
func lookupToken(ctx context.Context, host string, log *slog.Logger) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	token, source := auth.TokenForHost(host)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if token == "" {
		return "", &domain.UsageError{Msg: fmt.Sprintf(
			"no GitHub token for %s: set GH_TOKEN or GITHUB_TOKEN, or sign in with gh auth login", host)}
	}
	log.Debug("GitHub token", "host", host, "source", source)
	return token, nil
}

// gitBase checks the base URL that mirrors clone from. The token is sent to
// every URL under it, so it must be https without userinfo, a query or a
// fragment; plain http is allowed on a loopback host only, for tests.
func gitBase(raw, host string) (string, error) {
	if raw == "" {
		raw = "https://" + host
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return "", &domain.UsageError{Msg: fmt.Sprintf(
			"invalid GitHub git URL %q: want a scheme and a host, such as https://github.com", raw)}
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && loopback(u.Hostname()):
	default:
		return "", &domain.UsageError{Msg: fmt.Sprintf(
			"invalid GitHub git URL %q: the token is sent to it, so it must be https", raw)}
	}
	return u.Scheme + "://" + u.Host, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sleep waits for d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// redacted is how fmt and slog print credentials.
const redacted = "[redacted]"

// credentials are the environment variables that carry the token to git. A
// Source holds them behind a pointer because fmt cannot call a method of an
// unexported field: a []string field would print the token with %v, and
// TestGitHubSync caught exactly that. Inside a struct a pointer prints as
// an address; printed or logged on its own, credentials print as
// "[redacted]".
type credentials struct{ env []string }

// Format prints "[redacted]" for every verb.
func (*credentials) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// LogValue logs "[redacted]".
func (*credentials) LogValue() slog.Value { return slog.StringValue(redacted) }

// observer sees every API response before go-gh does. It records GitHub's
// clock from the Date header, so waits until a rate-limit reset are measured
// on GitHub's clock rather than a laptop's, which may be minutes off. It
// also keeps the rate-limit headers of the last response, because GitHub
// reports a spent budget and some secondary limits inside a 200, whose
// headers go-gh does not keep.
type observer struct {
	base  http.RoundTripper
	clock func() time.Time // the local clock: Config.Clock

	mu   sync.Mutex
	date time.Time   // the Date of the last response
	at   time.Time   // when that response arrived on clock, for the monotonic time since
	rate http.Header // the rate-limit headers of the last response; nil after a failed round trip
}

// rateHeaders are the response headers a rate limit is read from.
var rateHeaders = []string{"Retry-After", "X-Ratelimit-Remaining", "X-Ratelimit-Reset"}

// RoundTrip passes req to the base transport and notes the response's Date
// and rate-limit headers.
func (o *observer) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := o.base.RoundTrip(req)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rate = nil
	if err != nil {
		return nil, err
	}
	if date, perr := http.ParseTime(resp.Header.Get("Date")); perr == nil {
		o.date, o.at = date.UTC(), o.clock()
	}
	for _, k := range rateHeaders {
		if v := resp.Header.Get(k); v != "" {
			if o.rate == nil {
				o.rate = http.Header{}
			}
			o.rate.Set(k, v)
		}
	}
	return resp, nil
}

// headers returns the rate-limit headers of the last response. API requests
// go one at a time, behind the gate, so inside query they are the headers of
// the response that has just failed.
func (o *observer) headers() http.Header {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.rate
}

// now estimates GitHub's clock: the last Date plus the time since it
// arrived. ok is false before the first response with a Date.
func (o *observer) now() (t time.Time, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.date.IsZero() {
		return time.Time{}, false
	}
	return o.date.Add(o.clock().Sub(o.at)), true
}

// passStart is when a pass begins on GitHub's clock (observer.now), to the
// second below, which is the watermark a completed pass records: GitHub
// compares it with its own updatedAt values, so the laptop's clock, which
// may be minutes off, must not decide it. Before any response has given
// GitHub's clock, it is the local clock: the overlap a later pass reads
// below the watermark covers a small difference either way.
func (s *Source) passStart() time.Time {
	t, ok := s.seen.now()
	if !ok {
		t = s.clock()
	}
	return t.UTC().Truncate(time.Second)
}
