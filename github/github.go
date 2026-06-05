// Package github fetches GitHub pull-request status for a git branch via the
// gh CLI. Inline-thread resolved state and the reply/resolve mutations are only
// available through the GraphQL API, so we shell out to `gh api graphql`.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// runner executes a gh subcommand in dir and returns its stdout. It is a field
// so tests can inject canned responses.
type runner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// CmdError wraps a failed gh invocation, retaining stderr for classification.
type CmdError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CmdError) Error() string {
	return fmt.Sprintf("gh %s: %v: %s", strings.Join(e.Args, " "), e.Err, strings.TrimSpace(e.Stderr))
}

func (e *CmdError) Unwrap() error { return e.Err }

// defaultRunner runs the real gh binary.
func defaultRunner(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &CmdError{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

// IsNotInstalled reports whether err is because the gh binary is missing.
func IsNotInstalled(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}

// IsNotAuthenticated reports whether err is a gh authentication failure.
func IsNotAuthenticated(err error) bool {
	var ce *CmdError
	if !errors.As(err, &ce) {
		return false
	}
	s := strings.ToLower(ce.Stderr)
	return strings.Contains(s, "gh auth login") ||
		strings.Contains(s, "not logged in") ||
		strings.Contains(s, "authentication")
}

// FriendlyError turns a fetch error into a message suitable for the UI.
func FriendlyError(err error) string {
	switch {
	case err == nil:
		return ""
	case IsNotInstalled(err):
		return "GitHub CLI (gh) is not installed."
	case IsNotAuthenticated(err):
		return "Not authenticated with GitHub. Run: gh auth login"
	default:
		return "Failed to load pull request: " + err.Error()
	}
}

// Comment is a single comment, used for PR conversation comments and the
// comments within an inline review thread.
type Comment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	URL       string `json:"url"`
}

// ReviewSummary is a submitted review (approve / request-changes / comment).
type ReviewSummary struct {
	ID          string `json:"id"`
	Author      string `json:"author"`
	Body        string `json:"body"`
	State       string `json:"state"` // APPROVED | CHANGES_REQUESTED | COMMENTED | DISMISSED
	SubmittedAt string `json:"submitted_at"`
	URL         string `json:"url"`
}

// ReviewThread is an inline review thread. Only these can be resolved.
type ReviewThread struct {
	ID         string    `json:"id"` // node id for reply/resolve mutations
	IsResolved bool      `json:"is_resolved"`
	IsOutdated bool      `json:"is_outdated"`
	Path       string    `json:"path"`
	Line       int       `json:"line"`
	Comments   []Comment `json:"comments"`
}

// PRDetail is the full payload for the review panel.
type PRDetail struct {
	Number         int             `json:"number"`
	Title          string          `json:"title"`
	URL            string          `json:"url"`
	State          string          `json:"state"` // OPEN | CLOSED | MERGED
	IsDraft        bool            `json:"is_draft"`
	ReviewDecision string          `json:"review_decision"` // "" if none
	Author         string          `json:"author"`
	Conversation   []Comment       `json:"conversation"`
	Reviews        []ReviewSummary `json:"reviews"`
	Threads        []ReviewThread  `json:"threads"`
}

// PRSummary is the compact payload for the conversation-list badge. All fields
// are scalar so it is comparable (used to detect changes between polls).
type PRSummary struct {
	Number          int    `json:"number"`
	State           string `json:"state"`
	IsDraft         bool   `json:"is_draft"`
	ReviewDecision  string `json:"review_decision"`
	URL             string `json:"url"`
	UnresolvedCount int    `json:"unresolved_count"`
	CommentCount    int    `json:"comment_count"`
}

// Summary derives the list badge from the full detail.
func (d *PRDetail) Summary() PRSummary {
	s := PRSummary{
		Number:         d.Number,
		State:          d.State,
		IsDraft:        d.IsDraft,
		ReviewDecision: d.ReviewDecision,
		URL:            d.URL,
		CommentCount:   len(d.Conversation),
	}
	for _, t := range d.Threads {
		s.CommentCount += len(t.Comments)
		if !t.IsResolved {
			s.UnresolvedCount++
		}
	}
	return s
}

// resolveRepo returns the owner/name for the repository at repoRoot.
func resolveRepo(ctx context.Context, run runner, repoRoot string) (owner, name string, err error) {
	out, err := run(ctx, repoRoot, "repo", "view", "--json", "owner,name")
	if err != nil {
		return "", "", err
	}
	var r struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return "", "", fmt.Errorf("parse gh repo view: %w", err)
	}
	return r.Owner.Login, r.Name, nil
}

const prQuery = `query($owner:String!, $name:String!, $branch:String!) {
  repository(owner:$owner, name:$name) {
    pullRequests(headRefName:$branch, first:1, orderBy:{field:CREATED_AT, direction:DESC}) {
      nodes {
        number title url state isDraft reviewDecision
        author { login }
        comments(first:100) { nodes { id author { login } body createdAt url } }
        reviews(first:100) { nodes { id author { login } body state submittedAt url } }
        reviewThreads(first:100) {
          nodes {
            id isResolved isOutdated path line
            comments(first:50) { nodes { id author { login } body createdAt url } }
          }
        }
      }
    }
  }
}`

// fetchPR runs the GraphQL query for the most recent PR on branch. It returns
// (nil, nil) when the branch has no pull request.
func fetchPR(ctx context.Context, run runner, owner, name, branch string) (*PRDetail, error) {
	out, err := run(ctx, "", "api", "graphql",
		"-f", "query="+prQuery,
		"-f", "owner="+owner,
		"-f", "name="+name,
		"-f", "branch="+branch,
	)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Repository struct {
				PullRequests struct {
					Nodes []prNode `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse gh graphql: %w", err)
	}
	nodes := resp.Data.Repository.PullRequests.Nodes
	if len(nodes) == 0 {
		return nil, nil
	}
	return nodes[0].toDetail(), nil
}

const replyMutation = `mutation($threadId:ID!, $body:String!) {
  addPullRequestReviewThreadReply(input:{pullRequestReviewThreadId:$threadId, body:$body}) {
    comment { id }
  }
}`

// replyToThread posts a reply to an inline review thread.
func replyToThread(ctx context.Context, run runner, threadID, body string) error {
	_, err := run(ctx, "", "api", "graphql",
		"-f", "query="+replyMutation,
		"-f", "threadId="+threadID,
		"-f", "body="+body,
	)
	return err
}

const resolveMutation = `mutation($threadId:ID!) {
  resolveReviewThread(input:{threadId:$threadId}) { thread { id isResolved } }
}`

const unresolveMutation = `mutation($threadId:ID!) {
  unresolveReviewThread(input:{threadId:$threadId}) { thread { id isResolved } }
}`

// resolveThread resolves or unresolves an inline review thread.
func resolveThread(ctx context.Context, run runner, threadID string, resolved bool) error {
	q := resolveMutation
	if !resolved {
		q = unresolveMutation
	}
	_, err := run(ctx, "", "api", "graphql", "-f", "query="+q, "-f", "threadId="+threadID)
	return err
}

// commentOnPR posts a top-level conversation comment on the PR for branch.
func commentOnPR(ctx context.Context, run runner, repoRoot, branch, body string) error {
	_, err := run(ctx, repoRoot, "pr", "comment", branch, "--body", body)
	return err
}

// --- GraphQL response shapes ---

type ghAuthor struct {
	Login string `json:"login"`
}

func (a *ghAuthor) login() string {
	if a == nil {
		return ""
	}
	return a.Login
}

type ghComment struct {
	ID        string    `json:"id"`
	Author    *ghAuthor `json:"author"`
	Body      string    `json:"body"`
	CreatedAt string    `json:"createdAt"`
	URL       string    `json:"url"`
}

func (c ghComment) toComment() Comment {
	return Comment{ID: c.ID, Author: c.Author.login(), Body: c.Body, CreatedAt: c.CreatedAt, URL: c.URL}
}

type prNode struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	State          string    `json:"state"`
	IsDraft        bool      `json:"isDraft"`
	ReviewDecision string    `json:"reviewDecision"`
	Author         *ghAuthor `json:"author"`
	Comments       struct {
		Nodes []ghComment `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		Nodes []struct {
			ID          string    `json:"id"`
			Author      *ghAuthor `json:"author"`
			Body        string    `json:"body"`
			State       string    `json:"state"`
			SubmittedAt string    `json:"submittedAt"`
			URL         string    `json:"url"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		Nodes []struct {
			ID         string `json:"id"`
			IsResolved bool   `json:"isResolved"`
			IsOutdated bool   `json:"isOutdated"`
			Path       string `json:"path"`
			Line       int    `json:"line"`
			Comments   struct {
				Nodes []ghComment `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

func (n *prNode) toDetail() *PRDetail {
	d := &PRDetail{
		Number:         n.Number,
		Title:          n.Title,
		URL:            n.URL,
		State:          n.State,
		IsDraft:        n.IsDraft,
		ReviewDecision: n.ReviewDecision,
		Author:         n.Author.login(),
	}
	for _, c := range n.Comments.Nodes {
		d.Conversation = append(d.Conversation, c.toComment())
	}
	for _, r := range n.Reviews.Nodes {
		// Skip empty COMMENTED reviews (the wrapper around inline comments).
		if r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
			continue
		}
		d.Reviews = append(d.Reviews, ReviewSummary{
			ID: r.ID, Author: r.Author.login(), Body: r.Body,
			State: r.State, SubmittedAt: r.SubmittedAt, URL: r.URL,
		})
	}
	for _, t := range n.ReviewThreads.Nodes {
		thread := ReviewThread{
			ID: t.ID, IsResolved: t.IsResolved, IsOutdated: t.IsOutdated,
			Path: t.Path, Line: t.Line,
		}
		for _, c := range t.Comments.Nodes {
			thread.Comments = append(thread.Comments, c.toComment())
		}
		d.Threads = append(d.Threads, thread)
	}
	return d
}
