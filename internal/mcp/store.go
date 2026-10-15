package mcp

import (
	"context"
	"slices"
	"sync"

	"github.com/IHaveASegway/stillhere/internal/domain"
)

// cachedRuns is how many runs the cache keeps: the one the agent is asking
// about, and the one before it, in case answers from both are in flight
// when a scan publishes a new run.
const cachedRuns = 2

// cachedStore is a domain.FactStore that keeps what it read of a published
// run. A published run never changes (docs/spec/codebase.md#run-model:
// readers see complete and partial runs only, written once), so caching its
// facts is safe for as long as the process lives, and an agent's tenth
// question about a run reads it once instead of ten times. LatestRun is
// never cached, so a run a scan publishes is seen on the next call. It is
// the Decorator pattern (Gamma et al., Design Patterns) over the port: the
// use cases neither know nor care.
//
// Callers share the cached values, so they must treat them as read-only, as
// every read use case does (the read side computes nothing that changes a
// run). The server also runs one tool call at a time.
type cachedStore struct {
	domain.FactStore // writes, LatestRun and Close pass through

	mu    sync.Mutex
	runs  map[int64]*cachedRun
	order []int64 // the cached run ids, the most recently used last
}

// cachedRun is what the cache holds of one run; each part is filled on its
// first read.
type cachedRun struct {
	run    *domain.Run
	facts  *domain.FactSet
	values []domain.MetricValue
	ranks  []domain.RankInterval
	loaded bool // values and ranks were read

	watermarks []domain.RunWatermark
	marked     bool // the watermarks were read
	github     *domain.ForgeFacts
	kept       bool // the GitHub data the run kept was read
}

func newCachedStore(s domain.FactStore) *cachedStore {
	return &cachedStore{FactStore: s, runs: map[int64]*cachedRun{}}
}

// entry returns the cache entry for a run, making it the most recently
// used and evicting the least recently used beyond cachedRuns. The caller
// holds mu.
func (c *cachedStore) entry(id int64) *cachedRun {
	if i := slices.Index(c.order, id); i >= 0 {
		c.order = append(slices.Delete(c.order, i, i+1), id)
		return c.runs[id]
	}
	e := &cachedRun{}
	c.runs[id] = e
	c.order = append(c.order, id)
	for len(c.order) > cachedRuns {
		delete(c.runs, c.order[0])
		c.order = c.order[1:]
	}
	return e
}

// forget drops an entry that holds nothing, after a failed read: an
// unknown run id must not take a place in the cache. The caller holds mu.
func (c *cachedStore) forget(id int64) {
	if e := c.runs[id]; e != nil && e.run == nil && e.facts == nil && !e.loaded && !e.marked && !e.kept {
		delete(c.runs, id)
		c.order = slices.DeleteFunc(c.order, func(x int64) bool { return x == id })
	}
}

// LatestRun asks the store every time, so a run a scan published since the
// last call is seen, and keeps what it returns for the reads of that run
// that follow.
func (c *cachedStore) LatestRun(ctx context.Context, kind domain.RunKind) (domain.Run, error) {
	r, err := c.FactStore.LatestRun(ctx, kind)
	if err != nil {
		return r, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entry(r.ID); e.run == nil {
		e.run = &r
	}
	return r, nil
}

// Run returns a published run by id, read once.
func (c *cachedStore) Run(ctx context.Context, id int64) (domain.Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entry(id)
	if e.run == nil {
		r, err := c.FactStore.Run(ctx, id)
		if err != nil {
			c.forget(id)
			return r, err
		}
		e.run = &r
	}
	return *e.run, nil
}

// LoadFacts returns a published run's facts, read once.
func (c *cachedStore) LoadFacts(ctx context.Context, id int64) (*domain.FactSet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entry(id)
	if e.facts == nil {
		fs, err := c.FactStore.LoadFacts(ctx, id)
		if err != nil {
			c.forget(id)
			return nil, err
		}
		e.facts = fs
	}
	return e.facts, nil
}

// LoadMetrics returns a published run's values and rank intervals, read
// once.
func (c *cachedStore) LoadMetrics(ctx context.Context, id int64) ([]domain.MetricValue, []domain.RankInterval, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entry(id)
	if !e.loaded {
		values, ranks, err := c.FactStore.LoadMetrics(ctx, id)
		if err != nil {
			c.forget(id)
			return nil, nil, err
		}
		e.values, e.ranks, e.loaded = values, ranks, true
	}
	return e.values, e.ranks, nil
}

// cachedForge is the forge port over the same cache. What a published run
// read of the replica, its watermarks and the GitHub data it kept, is as
// immutable as its facts (ADR 0011), so it is read once per run too: an
// agent's who_knows calls on a large organization read the pull requests
// once, not on every call and twice in each (the run's names, then the
// answer). The replica itself, which a sync moves on, and every write pass
// through; no use case of the server reads or writes them.
type cachedForge struct {
	domain.ForgeStore
	c *cachedStore
}

// RunWatermarks returns a published run's forge watermarks, read once.
func (f *cachedForge) RunWatermarks(ctx context.Context, id int64) ([]domain.RunWatermark, error) {
	c := f.c
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entry(id)
	if !e.marked {
		marks, err := f.ForgeStore.RunWatermarks(ctx, id)
		if err != nil {
			c.forget(id)
			return nil, err
		}
		e.watermarks, e.marked = marks, true
	}
	return e.watermarks, nil
}

// RunForge returns the GitHub data a published run kept, read once.
func (f *cachedForge) RunForge(ctx context.Context, id int64) (*domain.ForgeFacts, error) {
	c := f.c
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entry(id)
	if !e.kept {
		github, err := f.ForgeStore.RunForge(ctx, id)
		if err != nil {
			c.forget(id)
			return nil, err
		}
		e.github, e.kept = github, true
	}
	return e.github, nil
}
