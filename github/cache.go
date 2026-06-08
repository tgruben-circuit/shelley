package github

import (
	"context"
	"sync"
	"time"

	"tailscale.com/util/singleflight"
)

// Cache stores PR detail keyed by (repoRoot, branch). It collapses concurrent
// fetches with singleflight and caches the owner/name per repo. "No PR for this
// branch" is cached as a nil detail with a nil error (negative cache).
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry // key: repoRoot + "\x00" + branch
	repos   map[string]repoID      // key: repoRoot
	run     runner
	now     func() time.Time
	sf      singleflight.Group[string, *PRDetail]
}

type cacheEntry struct {
	detail    *PRDetail
	err       error
	fetchedAt time.Time
}

type repoID struct {
	owner, name string
}

// NewCache returns a Cache backed by the real gh CLI.
func NewCache() *Cache {
	return &Cache{
		entries: make(map[string]*cacheEntry),
		repos:   make(map[string]repoID),
		run:     defaultRunner,
		now:     time.Now,
	}
}

func key(repoRoot, branch string) string { return repoRoot + "\x00" + branch }

// Peek returns the cached summary for (repoRoot, branch) without ever calling
// gh. It returns nil if nothing is cached or the branch has no PR.
func (c *Cache) Peek(repoRoot, branch string) *PRSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key(repoRoot, branch)]
	if !ok || e.detail == nil {
		return nil
	}
	s := e.detail.Summary()
	return &s
}

// Get returns cached detail if it is younger than maxAge, otherwise fetches it.
// Passing maxAge of 0 forces a refresh.
func (c *Cache) Get(ctx context.Context, repoRoot, branch string, maxAge time.Duration) (*PRDetail, error) {
	k := key(repoRoot, branch)
	c.mu.RLock()
	e, ok := c.entries[k]
	c.mu.RUnlock()
	if ok && c.now().Sub(e.fetchedAt) < maxAge {
		return e.detail, e.err
	}
	detail, err, _ := c.sf.Do(k, func() (*PRDetail, error) {
		return c.fetch(ctx, repoRoot, branch)
	})
	return detail, err
}

// Refresh forces a fetch and updates the cache.
func (c *Cache) Refresh(ctx context.Context, repoRoot, branch string) (*PRDetail, error) {
	return c.Get(ctx, repoRoot, branch, 0)
}

// Reply posts a reply to an inline review thread, then returns refreshed detail.
func (c *Cache) Reply(ctx context.Context, repoRoot, branch, threadID, body string) (*PRDetail, error) {
	if err := replyToThread(ctx, c.run, threadID, body); err != nil {
		return nil, err
	}
	return c.Refresh(ctx, repoRoot, branch)
}

// Resolve resolves or unresolves an inline review thread, then returns refreshed detail.
func (c *Cache) Resolve(ctx context.Context, repoRoot, branch, threadID string, resolved bool) (*PRDetail, error) {
	if err := resolveThread(ctx, c.run, threadID, resolved); err != nil {
		return nil, err
	}
	return c.Refresh(ctx, repoRoot, branch)
}

// Comment posts a top-level conversation comment, then returns refreshed detail.
func (c *Cache) Comment(ctx context.Context, repoRoot, branch, body string) (*PRDetail, error) {
	if err := commentOnPR(ctx, c.run, repoRoot, branch, body); err != nil {
		return nil, err
	}
	return c.Refresh(ctx, repoRoot, branch)
}

func (c *Cache) fetch(ctx context.Context, repoRoot, branch string) (*PRDetail, error) {
	owner, name, err := c.repoForRoot(ctx, repoRoot)
	if err != nil {
		c.store(repoRoot, branch, nil, err)
		return nil, err
	}
	detail, err := fetchPR(ctx, c.run, owner, name, branch)
	c.store(repoRoot, branch, detail, err)
	return detail, err
}

func (c *Cache) repoForRoot(ctx context.Context, repoRoot string) (owner, name string, err error) {
	c.mu.RLock()
	r, ok := c.repos[repoRoot]
	c.mu.RUnlock()
	if ok {
		return r.owner, r.name, nil
	}
	owner, name, err = resolveRepo(ctx, c.run, repoRoot)
	if err != nil {
		return "", "", err
	}
	c.mu.Lock()
	c.repos[repoRoot] = repoID{owner: owner, name: name}
	c.mu.Unlock()
	return owner, name, nil
}

func (c *Cache) store(repoRoot, branch string, d *PRDetail, err error) {
	c.mu.Lock()
	c.entries[key(repoRoot, branch)] = &cacheEntry{detail: d, err: err, fetchedAt: c.now()}
	c.mu.Unlock()
}
