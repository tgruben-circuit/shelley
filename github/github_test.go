package github

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunner dispatches on the gh subcommand. graphql returns prJSON, repo view
// returns a fixed owner/name.
func fakeRunner(prJSON string, calls *int32) runner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		switch {
		case len(args) >= 2 && args[0] == "repo" && args[1] == "view":
			return []byte(`{"owner":{"login":"acme"},"name":"widget"}`), nil
		case len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return []byte(prJSON), nil
		}
		return nil, errors.New("unexpected gh call: " + strings.Join(args, " "))
	}
}

const fullPR = `{"data":{"repository":{"pullRequests":{"nodes":[{
  "number":142,"title":"Add widget","url":"https://gh/142","state":"OPEN","isDraft":false,"reviewDecision":"CHANGES_REQUESTED",
  "author":{"login":"alice"},
  "comments":{"nodes":[{"id":"c1","author":{"login":"bob"},"body":"nice","createdAt":"t","url":"u"}]},
  "reviews":{"nodes":[
    {"id":"r1","author":{"login":"bob"},"body":"please fix","state":"CHANGES_REQUESTED","submittedAt":"t","url":"u"},
    {"id":"r2","author":{"login":"bob"},"body":"","state":"COMMENTED","submittedAt":"t","url":"u"}
  ]},
  "reviewThreads":{"nodes":[
    {"id":"t1","isResolved":false,"isOutdated":false,"path":"a.go","line":10,"comments":{"nodes":[{"id":"tc1","author":{"login":"bob"},"body":"bug here","createdAt":"t","url":"u"}]}},
    {"id":"t2","isResolved":true,"isOutdated":false,"path":"b.go","line":20,"comments":{"nodes":[{"id":"tc2","author":{"login":"bob"},"body":"resolved one","createdAt":"t","url":"u"}]}}
  ]}
}]}}}}`

const noPR = `{"data":{"repository":{"pullRequests":{"nodes":[]}}}}`

func TestFetchPRParsesAllThreeCommentKinds(t *testing.T) {
	d, err := fetchPR(context.Background(), fakeRunner(fullPR, nil), "acme", "widget", "feature")
	if err != nil {
		t.Fatalf("fetchPR: %v", err)
	}
	if d == nil {
		t.Fatal("expected detail, got nil")
	}
	if d.Number != 142 || d.State != "OPEN" || d.ReviewDecision != "CHANGES_REQUESTED" {
		t.Errorf("unexpected header: %+v", d)
	}
	if len(d.Conversation) != 1 || d.Conversation[0].Author != "bob" {
		t.Errorf("conversation comments: %+v", d.Conversation)
	}
	// The empty COMMENTED review wrapper is dropped, leaving the real review.
	if len(d.Reviews) != 1 || d.Reviews[0].State != "CHANGES_REQUESTED" {
		t.Errorf("reviews: %+v", d.Reviews)
	}
	if len(d.Threads) != 2 {
		t.Fatalf("threads: %+v", d.Threads)
	}
	if d.Threads[0].IsResolved || !d.Threads[1].IsResolved {
		t.Errorf("resolved flags wrong: %+v", d.Threads)
	}
}

func TestSummaryDerivation(t *testing.T) {
	d, _ := fetchPR(context.Background(), fakeRunner(fullPR, nil), "acme", "widget", "feature")
	s := d.Summary()
	if s.UnresolvedCount != 1 {
		t.Errorf("UnresolvedCount = %d, want 1", s.UnresolvedCount)
	}
	// 1 conversation comment + 2 thread comments.
	if s.CommentCount != 3 {
		t.Errorf("CommentCount = %d, want 3", s.CommentCount)
	}
	if s.Number != 142 || s.State != "OPEN" {
		t.Errorf("summary header: %+v", s)
	}
}

func TestFetchPRNoPR(t *testing.T) {
	d, err := fetchPR(context.Background(), fakeRunner(noPR, nil), "acme", "widget", "feature")
	if err != nil {
		t.Fatalf("fetchPR: %v", err)
	}
	if d != nil {
		t.Errorf("expected nil detail for no PR, got %+v", d)
	}
}

func TestErrorClassification(t *testing.T) {
	notFound := &CmdError{Args: []string{"repo"}, Err: exec.ErrNotFound}
	if !IsNotInstalled(notFound) {
		t.Error("expected IsNotInstalled")
	}
	authErr := &CmdError{Args: []string{"api"}, Stderr: "gh auth login required", Err: errors.New("exit 1")}
	if !IsNotAuthenticated(authErr) {
		t.Error("expected IsNotAuthenticated")
	}
	if IsNotInstalled(authErr) {
		t.Error("auth error should not be not-installed")
	}
	if got := FriendlyError(notFound); !strings.Contains(got, "not installed") {
		t.Errorf("FriendlyError = %q", got)
	}
}

func TestCacheNegativeAndPeek(t *testing.T) {
	var calls int32
	c := NewCache()
	c.run = fakeRunner(noPR, &calls)
	c.now = func() time.Time { return time.Unix(0, 0) }

	if got := c.Peek("/repo", "feature"); got != nil {
		t.Errorf("Peek before fetch should be nil, got %+v", got)
	}
	d, err := c.Get(context.Background(), "/repo", "feature", time.Minute)
	if err != nil || d != nil {
		t.Fatalf("Get no-PR = (%+v, %v)", d, err)
	}
	// Negative result is cached: Peek still nil, but a subsequent fresh Get does
	// not call gh again.
	before := atomic.LoadInt32(&calls)
	if _, _ = c.Get(context.Background(), "/repo", "feature", time.Minute); atomic.LoadInt32(&calls) != before {
		t.Error("expected no gh calls on cache hit")
	}
}

func TestCacheReusesRepoLookupAndPeekSummary(t *testing.T) {
	var calls int32
	c := NewCache()
	c.run = fakeRunner(fullPR, &calls)
	c.now = func() time.Time { return time.Unix(0, 0) }

	if _, err := c.Refresh(context.Background(), "/repo", "feature"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	s := c.Peek("/repo", "feature")
	if s == nil || s.Number != 142 || s.UnresolvedCount != 1 {
		t.Fatalf("Peek summary = %+v", s)
	}
	// First Refresh = repo view + graphql (2 calls). Second Refresh reuses the
	// cached repo id, so only 1 more call.
	c.Refresh(context.Background(), "/repo", "feature")
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("gh calls = %d, want 3 (repo+graphql, then graphql)", got)
	}
}

func TestMutationsCallGhThenRefresh(t *testing.T) {
	cases := []struct {
		name      string
		do        func(c *Cache) (*PRDetail, error)
		wantQuery string // substring expected in the graphql query, or "" for gh pr comment
		wantArgs  []string
	}{
		{
			name:      "reply",
			do:        func(c *Cache) (*PRDetail, error) { return c.Reply(ctx0(), "/repo", "feat", "T1", "thanks") },
			wantQuery: "addPullRequestReviewThreadReply",
		},
		{
			name:      "resolve",
			do:        func(c *Cache) (*PRDetail, error) { return c.Resolve(ctx0(), "/repo", "feat", "T1", true) },
			wantQuery: "resolveReviewThread",
		},
		{
			name:      "unresolve",
			do:        func(c *Cache) (*PRDetail, error) { return c.Resolve(ctx0(), "/repo", "feat", "T1", false) },
			wantQuery: "unresolveReviewThread",
		},
		{
			name:     "comment",
			do:       func(c *Cache) (*PRDetail, error) { return c.Comment(ctx0(), "/repo", "feat", "hello") },
			wantArgs: []string{"pr", "comment", "feat", "--body", "hello"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mutationArgs []string
			var refreshed bool
			c := NewCache()
			c.now = func() time.Time { return time.Unix(0, 0) }
			c.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				switch {
				case args[0] == "repo":
					return []byte(`{"owner":{"login":"acme"},"name":"widget"}`), nil
				case args[0] == "api" && strings.Contains(strings.Join(args, " "), "pullRequests("):
					// the post-mutation Refresh query
					refreshed = true
					return []byte(fullPR), nil
				default:
					mutationArgs = args
					return []byte(`{}`), nil
				}
			}
			d, err := tc.do(c)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if d == nil || d.Number != 142 {
				t.Errorf("%s: expected refreshed detail, got %+v", tc.name, d)
			}
			if !refreshed {
				t.Errorf("%s: expected a refresh after mutation", tc.name)
			}
			joined := strings.Join(mutationArgs, " ")
			if tc.wantQuery != "" && !strings.Contains(joined, tc.wantQuery) {
				t.Errorf("%s: mutation args %q missing %q", tc.name, joined, tc.wantQuery)
			}
			for _, a := range tc.wantArgs {
				if !strings.Contains(joined, a) {
					t.Errorf("%s: mutation args %q missing %q", tc.name, joined, a)
				}
			}
		})
	}
}

func ctx0() context.Context { return context.Background() }

func TestCacheSingleflightCollapses(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	c := NewCache()
	c.now = func() time.Time { return time.Unix(0, 0) }
	c.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "repo" {
			return []byte(`{"owner":{"login":"acme"},"name":"widget"}`), nil
		}
		atomic.AddInt32(&calls, 1)
		<-release // block until released so concurrent calls overlap
		return []byte(fullPR), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Refresh(context.Background(), "/repo", "feature")
		}()
	}
	// Give the goroutines time to coalesce on the singleflight key, then release.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("graphql calls = %d, want 1 (collapsed)", got)
	}
}
