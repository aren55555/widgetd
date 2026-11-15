// Package server is the github-notifications widget's server half. Every
// so often (as often as GitHub's X-Poll-Interval allows) it reads the
// signed-in account's open pull requests with their status, the pull
// requests waiting on its review, and the issue threads that changed since
// it last read them. It never sees the account's token: requests it marks
// with widget.Authorize get it from the host.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/github-notifications/gen/ghnotif/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	api         = "https://api.github.com"
	defaultPoll = 60 * time.Second
	maxRows     = 30
)

// Run is the widget's server half.
func Run(ctx context.Context, h widget.Host) error {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	card, err := widget.MountState[*pb.State](h, "inbox", widget.MountOptions{
		Layout: widget.Layout{Cols: 2, Rows: 1},
	})
	if err != nil {
		return err
	}

	gh := &client{h: h, cache: map[string]cached{}, used: map[string]bool{}, orgs: map[string]bool{}}
	st := &pb.State{}
	var last *pb.State
	for {
		wait := defaultPoll
		// A section that can't be read keeps what it showed last.
		if prs, err := gh.mine(ctx); err != nil {
			h.Logger().Warn("github-notifications: reading your pull requests failed", "err", err)
		} else {
			st.Mine = prs
		}
		if prs, err := gh.reviews(ctx); err != nil {
			h.Logger().Warn("github-notifications: reading review requests failed", "err", err)
		} else {
			st.Reviews = prs
		}
		if issues, poll, err := gh.issues(ctx, cfg.GetParticipating()); err != nil {
			h.Logger().Warn("github-notifications: reading notifications failed", "err", err)
		} else {
			st.Issues = issues
			if poll > 0 {
				wait = poll
			}
		}
		gh.sweep()
		if !proto.Equal(st, last) {
			if err := card.Snapshot(st); err != nil {
				return err
			}
			last = proto.Clone(st).(*pb.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// client reads the GitHub API, remembering each reply's ETag: asking again
// with it costs nothing against the rate limit when nothing changed.
type client struct {
	h     widget.Host
	cache map[string]cached
	used  map[string]bool // paths read since the last sweep
	orgs  map[string]bool // whether each repo's owner is an org, this round
}

// org reports whether a repo ("owner/name") belongs to an organization.
// It asks once per repo a round; if it can't tell, it says no.
func (c *client) org(ctx context.Context, repo string) bool {
	if v, ok := c.orgs[repo]; ok {
		return v
	}
	var r struct {
		Owner struct {
			Type string `json:"type"`
		} `json:"owner"`
	}
	if _, err := c.get(ctx, "/repos/"+repo, &r); err != nil {
		c.h.Logger().Warn("github-notifications: reading a repo's owner failed", "repo", repo, "err", err)
	}
	c.orgs[repo] = r.Owner.Type == "Organization"
	return c.orgs[repo]
}

// searchItem is a pull request as GitHub's issue search returns it.
type searchItem struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	// "https://api.github.com/repos/owner/name"
	RepositoryURL string `json:"repository_url"`
}

func (it *searchItem) repo() string {
	return strings.TrimPrefix(it.RepositoryURL, api+"/repos/")
}

func (it *searchItem) pr() *pb.PullRequest {
	return &pb.PullRequest{
		Repo: clip(it.repo(), 140), Number: int32(it.Number), Title: clip(it.Title, 300),
		Author: clip(it.User.Login, 64), Draft: it.Draft,
		CreatedUnixMs: it.CreatedAt.UnixMilli(), UpdatedUnixMs: it.UpdatedAt.UnixMilli(),
	}
}

func (c *client) search(ctx context.Context, q, sort, order string) ([]searchItem, error) {
	var res struct {
		Items []searchItem `json:"items"`
	}
	v := url.Values{"q": {q}, "sort": {sort}, "order": {order}, "per_page": {strconv.Itoa(maxRows)}}
	if _, err := c.get(ctx, "/search/issues?"+v.Encode(), &res); err != nil {
		return nil, err
	}
	return res.Items, nil
}

// mine is the account's open pull requests, most recently updated first,
// each with its checks, review and whether it conflicts with its base.
func (c *client) mine(ctx context.Context) ([]*pb.PullRequest, error) {
	items, err := c.search(ctx, "is:pr is:open archived:false author:@me", "updated", "desc")
	if err != nil {
		return nil, err
	}
	out := make([]*pb.PullRequest, 0, len(items))
	for i := range items {
		pr := items[i].pr()
		pr.Org = c.org(ctx, pr.GetRepo())
		if err := c.status(ctx, pr); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, nil
}

// reviews is the open pull requests waiting on the account's review (or a
// team's it's on), oldest first.
func (c *client) reviews(ctx context.Context) ([]*pb.PullRequest, error) {
	items, err := c.search(ctx, "is:pr is:open archived:false review-requested:@me", "created", "asc")
	if err != nil {
		return nil, err
	}
	out := make([]*pb.PullRequest, 0, len(items))
	for i := range items {
		pr := items[i].pr()
		pr.Org = c.org(ctx, pr.GetRepo())
		out = append(out, pr)
	}
	return out, nil
}

// status fills in a pull request's checks, review and conflicts.
func (c *client) status(ctx context.Context, pr *pb.PullRequest) error {
	base := fmt.Sprintf("/repos/%s/pulls/%d", pr.GetRepo(), pr.GetNumber())
	var full struct {
		Mergeable *bool `json:"mergeable"` // null while GitHub works it out
		Head      struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if _, err := c.get(ctx, base, &full); err != nil {
		return err
	}
	pr.Conflicts = full.Mergeable != nil && !*full.Mergeable

	var reviews []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State string `json:"state"`
	}
	if _, err := c.get(ctx, base+"/reviews?per_page=100", &reviews); err != nil {
		return err
	}
	// Each reviewer's latest verdict counts; a comment doesn't undo one.
	verdict := map[string]string{}
	commented := false
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			verdict[r.User.Login] = r.State
		case "COMMENTED":
			commented = true
		}
	}
	states := make([]string, 0, len(verdict))
	for _, s := range verdict {
		states = append(states, s)
	}
	switch {
	case slices.Contains(states, "CHANGES_REQUESTED"):
		pr.Review = pb.Review_REVIEW_CHANGES_REQUESTED
	case slices.Contains(states, "APPROVED"):
		pr.Review = pb.Review_REVIEW_APPROVED
	case commented:
		pr.Review = pb.Review_REVIEW_COMMENTED
	}

	if full.Head.SHA == "" {
		return nil
	}
	commit := fmt.Sprintf("/repos/%s/commits/%s", pr.GetRepo(), full.Head.SHA)
	var runs struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if _, err := c.get(ctx, commit+"/check-runs?per_page=100", &runs); err != nil {
		return err
	}
	// Commit statuses are the older API some CI still reports through.
	var combined struct {
		State    string `json:"state"`
		Statuses []any  `json:"statuses"`
	}
	if _, err := c.get(ctx, commit+"/status", &combined); err != nil {
		return err
	}
	// "pending" with no statuses means no CI reports that way at all.
	ran := len(combined.Statuses) > 0
	pending := ran && combined.State == "pending"
	failing := combined.State == "failure" || combined.State == "error"
	for _, r := range runs.CheckRuns {
		ran = true
		if r.Status != "completed" {
			pending = true
			continue
		}
		switch r.Conclusion {
		case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
			failing = true
		}
	}
	switch {
	case failing:
		pr.Checks = pb.Checks_CHECKS_FAILING
	case pending:
		pr.Checks = pb.Checks_CHECKS_PENDING
	case ran:
		pr.Checks = pb.Checks_CHECKS_PASSING
	}
	return nil
}

// issues is the unread issue threads, newest first, and how long GitHub
// asks pollers of notifications to wait (0 if it didn't say). An issue
// thread is unread until the account opens it, so these are exactly the
// threads that changed since it last looked.
func (c *client) issues(ctx context.Context, participating bool) ([]*pb.Issue, time.Duration, error) {
	path := "/notifications?per_page=50"
	if participating {
		path += "&participating=true"
	}
	var raw []struct {
		Reason    string    `json:"reason"`
		UpdatedAt time.Time `json:"updated_at"`
		Subject   struct {
			Title string `json:"title"`
			Type  string `json:"type"`
			// "https://api.github.com/repos/owner/name/issues/123"
			URL string `json:"url"`
		} `json:"subject"`
		Repository struct {
			FullName string `json:"full_name"`
			Owner    struct {
				Type string `json:"type"` // "User" or "Organization"
			} `json:"owner"`
		} `json:"repository"`
	}
	hdr, err := c.get(ctx, path, &raw)
	var poll time.Duration
	if s, perr := strconv.Atoi(hdr.Get("X-Poll-Interval")); perr == nil && s > 0 {
		poll = time.Duration(s) * time.Second
	}
	if err != nil {
		return nil, poll, err
	}
	out := []*pb.Issue{}
	for _, n := range raw {
		if n.Subject.Type != "Issue" {
			continue
		}
		if len(out) == maxRows {
			break
		}
		out = append(out, &pb.Issue{
			Repo: clip(n.Repository.FullName, 140), Title: clip(n.Subject.Title, 300),
			Reason: clip(n.Reason, 40), UpdatedUnixMs: n.UpdatedAt.UnixMilli(),
			Number: issueNumber(n.Subject.URL), Org: n.Repository.Owner.Type == "Organization",
		})
	}
	return out, poll, nil
}

// issueNumber is the number at the end of an issue's API URL, or 0.
func issueNumber(u string) int32 {
	_, tail, ok := strings.Cut(u, "/issues/")
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(tail, 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}

// sweep forgets replies not asked for since the last sweep (closed PRs,
// old commits), so the cache holds one round's worth.
func (c *client) sweep() {
	for p := range c.cache {
		if !c.used[p] {
			delete(c.cache, p)
		}
	}
	c.used = map[string]bool{}
	c.orgs = map[string]bool{}
}

type cached struct {
	etag string
	body []byte
}

// get reads a signed GitHub API path as JSON into into, and returns the
// reply's headers (empty if there was no reply).
func (c *client) get(ctx context.Context, path string, into any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+path, http.NoBody)
	if err != nil {
		return http.Header{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "widgetd-github-notifications")
	c.used[path] = true
	prev, ok := c.cache[path]
	if ok {
		req.Header.Set("If-None-Match", prev.etag)
	}
	resp, err := c.h.Fetch(widget.Authorize(req))
	if err != nil {
		return http.Header{}, err
	}
	defer resp.Body.Close()
	var body []byte
	switch {
	case resp.StatusCode == http.StatusNotModified && ok:
		body = prev.body
	case resp.StatusCode == http.StatusOK:
		if body, err = io.ReadAll(resp.Body); err != nil {
			return resp.Header, err
		}
		if etag := resp.Header.Get("ETag"); etag != "" {
			c.cache[path] = cached{etag: etag, body: body}
		}
	default:
		return resp.Header, fmt.Errorf("%s: %s", path, resp.Status)
	}
	return resp.Header, json.Unmarshal(body, into)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
