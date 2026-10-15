package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/IHaveASegway/stillhere/internal/domain"
	"github.com/IHaveASegway/stillhere/internal/redact"
)

// How the client paces itself (docs.github.com, "Rate limits and query
// limits for the GraphQL API" and "Best practices for using the REST API").
const (
	// reserve is the point budget left untouched before the hourly reset,
	// for the user's other tools on the same token (gh, an editor). A
	// stillhere query costs 1 to 3 points.
	reserve = 100

	// maxAttempts bounds the tries of one request, rate-limit errors
	// included. The wait before a request when the budget is nearly spent
	// (awaitBudget) is not an attempt: it always ends.
	maxAttempts = 6

	// A server error or a dropped connection is retried after
	// transientBase, doubling up to transientCap.
	transientBase = 2 * time.Second
	transientCap  = time.Minute

	// A secondary rate limit without a Retry-After is retried after at
	// least a minute, as GitHub asks, doubling up to secondaryCap.
	secondaryBase = 2 * time.Minute
	secondaryCap  = 15 * time.Minute

	// resetMargin is added to every wait for a reset GitHub announced, so
	// the request after it does not arrive a moment early.
	resetMargin = time.Second

	// minPage is the smallest pull request page a timeout shrinks to.
	minPage = 5
)

// budget is the rate-limit state of the last successful response.
type budget struct {
	remaining int       // points left in the window; -1 when unknown
	reset     time.Time // when the window resets, on GitHub's clock
}

// rateLimitJSON is the rateLimit object every query asks for: what the
// query cost and what is left.
type rateLimitJSON struct {
	Cost      int       `json:"cost"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

// rated is a response that carries its rateLimit object.
type rated interface{ rate() rateLimitJSON }

// apiError is a GitHub API failure. Its message is scrubbed of anything that
// looks like a credential; errors.As still reaches go-gh's error beneath it.
type apiError struct {
	op  string // the GraphQL operation, such as "PullRequests"
	msg string // redact.Message of the cause
	err error
}

func (e *apiError) Error() string { return "GitHub " + e.op + ": " + e.msg }

func (e *apiError) Unwrap() error { return e.err }

// query sends one GraphQL operation and decodes its data into a new T,
// retrying through rate limits and transient failures. API requests take
// the gate one at a time, as GitHub asks of clients, and a wait holds the
// gate, because a rate limit applies to the whole token, not to one caller.
//
// When shrink is true, a server error or a timeout halves the "first"
// variable before the retry, down to minPage: GitHub's advice for a query
// that times out is a smaller page. The caller reads the new size from vars.
func query[T any, P interface {
	*T
	rated
}](ctx context.Context, s *Source, op, document string, vars map[string]any, shrink bool) (*T, error) {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.gate }()

	for attempt := 1; ; attempt++ {
		if err := s.awaitBudget(ctx); err != nil {
			return nil, err
		}
		out := P(new(T))
		err := s.api.DoWithContext(ctx, document, vars, out)
		if err == nil {
			s.spent(ctx, op, out.rate())
			return (*T)(out), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		wait, reason, transient := s.backoff(err, attempt)
		if reason == "" || attempt >= maxAttempts {
			return nil, failure(op, err)
		}
		if shrink && transient {
			if first, ok := vars["first"].(int); ok && first > minPage {
				vars["first"] = max(first/2, minPage)
			}
		}
		s.log.Info("GitHub API: waiting before a retry",
			"query", op, "reason", reason, "wait", wait.Round(time.Second), "attempt", attempt)
		if err := s.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// spent records the budget a successful response reported. A response
// without a rateLimit object leaves the budget unknown rather than empty.
func (s *Source) spent(ctx context.Context, op string, rl rateLimitJSON) {
	b := budget{remaining: rl.Remaining, reset: rl.ResetAt.UTC()}
	if rl.ResetAt.IsZero() {
		b = budget{remaining: -1}
	}
	s.mu.Lock()
	s.budget = b
	s.mu.Unlock()
	s.log.LogAttrs(ctx, slog.LevelDebug, "GitHub API",
		slog.String("query", op), slog.Int("cost", rl.Cost), slog.Int("remaining", rl.Remaining))
}

// awaitBudget waits for the window to reset when the last response left
// fewer than reserve points. GitHub's primary limit is a point budget per
// hour; a client that keeps sending at zero gets errors and, if it insists,
// a secondary limit.
func (s *Source) awaitBudget(ctx context.Context) error {
	s.mu.Lock()
	b := s.budget
	s.mu.Unlock()
	if b.remaining < 0 || b.remaining >= reserve {
		return nil
	}
	wait := s.untilReset(b.reset)
	s.log.Info("GitHub API: the hourly points are nearly spent; waiting for the reset",
		"remaining", b.remaining, "reset", b.reset.Format(time.RFC3339), "wait", wait.Round(time.Second))
	if err := s.sleep(ctx, wait); err != nil {
		return err
	}
	s.mu.Lock()
	s.budget = budget{remaining: -1}
	s.mu.Unlock()
	return nil
}

// untilReset is how long to wait for a reset at the instant reset on
// GitHub's clock, plus a margin and up to a second of jitter, so clients
// that waited for the same reset do not all return at once.
func (s *Source) untilReset(reset time.Time) time.Duration {
	wait := resetMargin + jitter(time.Second)
	if now, ok := s.seen.now(); ok && reset.After(now) {
		wait += reset.Sub(now)
	}
	return wait
}

// backoff decides whether a failed request is worth another try and how
// long to wait first. reason is "" for a failure that a retry cannot fix,
// such as bad credentials or a missing repository. transient is true for a
// server error or a dropped connection, the failures a smaller page may
// avoid.
func (s *Source) backoff(err error, attempt int) (wait time.Duration, reason string, transient bool) {
	var herr *api.HTTPError
	var gerr *api.GraphQLError
	var uerr *url.Error
	switch {
	case errors.As(err, &herr):
		return s.httpBackoff(herr, attempt)
	case errors.As(err, &gerr):
		return s.graphQLBackoff(gerr, attempt)
	case errors.As(err, &uerr):
		// The connection failed or timed out before GitHub answered.
		return equalJitter(transientBase, transientCap, attempt), "network error", true
	}
	return 0, "", false
}

// httpBackoff classifies an HTTP error status the way GitHub documents it: a
// Retry-After header is obeyed, X-RateLimit-Remaining 0 waits for
// X-RateLimit-Reset, any other secondary limit waits at least a minute and
// then exponentially longer, and a server error is retried sooner.
func (s *Source) httpBackoff(herr *api.HTTPError, attempt int) (time.Duration, string, bool) {
	switch status := herr.StatusCode; {
	case status == http.StatusForbidden || status == http.StatusTooManyRequests:
		if wait, reason, ok := s.limitWait(herr.Headers); ok {
			return wait, reason, false
		}
		if status == http.StatusTooManyRequests || secondaryLimit(herr.Message) {
			return equalJitter(secondaryBase, secondaryCap, attempt), "secondary rate limit", false
		}
		return 0, "", false // forbidden: a scope, an SSO authorization or a permission is missing
	case status >= 500:
		return equalJitter(transientBase, transientCap, attempt), "HTTP " + strconv.Itoa(status), true
	}
	return 0, "", false
}

// graphQLBackoff classifies the errors of a response that GitHub sent with
// status 200. GitHub reports both rate limits that way too: a spent hourly
// budget as an error of type RATE_LIMITED with X-RateLimit-Remaining 0, and
// a secondary limit by its message. go-gh keeps no header of a 200, so the
// headers come from the observer. As for an HTTP error, Retry-After and
// X-RateLimit-Reset are obeyed first; without them, a spent budget waits
// for the reset the last success reported, and a secondary limit backs off
// from a minute up.
func (s *Source) graphQLBackoff(gerr *api.GraphQLError, attempt int) (time.Duration, string, bool) {
	secondary := slices.ContainsFunc(gerr.Errors, func(e api.GraphQLErrorItem) bool { return secondaryLimit(e.Message) })
	spent := slices.ContainsFunc(gerr.Errors, func(e api.GraphQLErrorItem) bool { return e.Type == "RATE_LIMITED" })
	if !secondary && !spent {
		return 0, "", false
	}
	if wait, reason, ok := s.limitWait(s.seen.headers()); ok {
		return wait, reason, false
	}
	if !secondary {
		s.mu.Lock()
		reset := s.budget.reset
		s.mu.Unlock()
		if now, ok := s.seen.now(); ok && reset.After(now) {
			return s.untilReset(reset), "rate limit exhausted", false
		}
		return equalJitter(secondaryBase, secondaryCap, attempt), "rate limit exhausted", false
	}
	return equalJitter(secondaryBase, secondaryCap, attempt), "secondary rate limit", false
}

// limitWait reads the wait a rate-limited response asks for from its
// headers: Retry-After seconds, else, once X-RateLimit-Remaining is 0,
// until X-RateLimit-Reset on GitHub's clock (docs.github.com, "Rate limits
// and query limits for the GraphQL API"). ok is false when they ask for
// neither.
func (s *Source) limitWait(h http.Header) (wait time.Duration, reason string, ok bool) {
	if seconds, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && seconds >= 0 {
		return time.Duration(seconds)*time.Second + jitter(time.Second), "rate limit (Retry-After)", true
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if epoch, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return s.untilReset(time.Unix(epoch, 0).UTC()), "rate limit exhausted", true
		}
	}
	return 0, "", false
}

// secondaryLimit reports whether a 403 message is GitHub's secondary rate
// limit, under its current name or the older "abuse detection" one.
func secondaryLimit(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "secondary rate limit") || strings.Contains(msg, "abuse detection")
}

// equalJitter is exponential backoff with "equal jitter" (Marc Brooker,
// "Exponential Backoff And Jitter", AWS Architecture Blog, 2015): the
// attempt's ceiling doubles from base up to ceil, and the wait is half the
// ceiling plus a random part of the other half. The fixed half keeps the
// floor GitHub asks for; the random half spreads out clients that failed
// together.
func equalJitter(base, ceil time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt && d < ceil; i++ {
		d *= 2
	}
	d = min(d, ceil)
	return d/2 + jitter(d/2)
}

// jitter returns a random duration in [0, d).
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return rand.N(d)
}

// failure turns a request that cannot succeed into the error the caller
// returns. Bad credentials are a *domain.UsageError, because the user fixes
// them by signing in again.
func failure(op string, err error) error {
	var herr *api.HTTPError
	if errors.As(err, &herr) && herr.StatusCode == http.StatusUnauthorized {
		return &domain.UsageError{Msg: "GitHub rejected the token (HTTP 401): " +
			"set GH_TOKEN or GITHUB_TOKEN, or sign in again with gh auth login"}
	}
	return &apiError{op: op, msg: redact.Message(err.Error()), err: err}
}

// pageInfo is a GraphQL connection's cursor state.
type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// cursor returns the "after" variable that continues a connection: JSON
// null to start at the beginning.
func cursor(after string) any {
	if after == "" {
		return nil
	}
	return after
}

// nextCursor returns the cursor of the page after one read from the cursor
// after ("" for the first page), for a connection whose pageInfo says it has
// a next page. A cursor that is missing or did not move would read the same
// page again, and so on forever, spending the budget down to the reserve
// and then waiting out reset after reset; the pass fails instead.
func nextCursor(op string, pi pageInfo, after string) (string, error) {
	if pi.EndCursor == "" || pi.EndCursor == after {
		return "", fmt.Errorf("GitHub %s: the connection says it has another page, but its cursor does not move past this one", op)
	}
	return pi.EndCursor, nil
}

// errNoNode is returned when GitHub answers a query about a repository or a
// pull request with null: deleted, transferred, or not visible to the token.
func errNoNode(kind, id string) error {
	return fmt.Errorf("GitHub has no %s %s visible to this token", kind, id)
}
