// Package server is the github-notifications widget's server half. It
// reads the signed-in account's unread notifications every so often (as
// often as GitHub's X-Poll-Interval allows) and shows the count and the
// latest few on one card. It never sees the account's token: requests it
// marks with widget.Authorize get it from the host.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/github-notifications/gen/ghnotif/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	api         = "https://api.github.com"
	defaultPoll = 60 * time.Second
	shown       = 6
)

// Run is the widget's server half.
func Run(ctx context.Context, h widget.Host) error {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	var user struct {
		Login string `json:"login"`
	}
	if _, _, err := get(ctx, h, "/user", "", &user); err != nil {
		return err
	}
	card, err := widget.MountState[*pb.State](h, "inbox", widget.MountOptions{
		Title:  "GitHub · @" + user.Login,
		Layout: widget.Layout{Cols: 2, Rows: 1},
	})
	if err != nil {
		return err
	}

	var last *pb.State
	var lastModified string
	for {
		path := "/notifications?per_page=50"
		if cfg.GetParticipating() {
			path += "&participating=true"
		}
		var raw []struct {
			Reason    string    `json:"reason"`
			UpdatedAt time.Time `json:"updated_at"`
			Subject   struct {
				Title string `json:"title"`
				Type  string `json:"type"`
			} `json:"subject"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		hdr, changed, err := get(ctx, h, path, lastModified, &raw)
		wait := defaultPoll
		if hdr != nil {
			if s, err := strconv.Atoi(hdr.Get("X-Poll-Interval")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			if lm := hdr.Get("Last-Modified"); lm != "" {
				lastModified = lm
			}
		}
		switch {
		case err != nil:
			h.Logger().Warn("github-notifications: read failed", "err", err)
		case changed:
			st := &pb.State{Login: user.Login, Unread: int32(len(raw))}
			for i, n := range raw {
				if i == shown {
					break
				}
				st.Items = append(st.Items, &pb.Item{
					Repo: clip(n.Repository.FullName, 140), Title: clip(n.Subject.Title, 300),
					Kind: clip(n.Subject.Type, 40), Reason: clip(n.Reason, 40), UpdatedUnixMs: n.UpdatedAt.UnixMilli(),
				})
			}
			if !proto.Equal(st, last) {
				if err := card.Snapshot(st); err != nil {
					return err
				}
				last = st
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// get reads a signed GitHub API path as JSON, and returns the reply's
// headers (nil if there was none). With since, an unchanged
// answer is a 304 that costs nothing against the rate limit: changed is
// then false and into is left alone.
func get(ctx context.Context, h widget.Host, path, since string, into any) (hdr http.Header, changed bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+path, http.NoBody)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "widgetd-github-notifications")
	if since != "" {
		req.Header.Set("If-Modified-Since", since)
	}
	resp, err := h.Fetch(widget.Authorize(req))
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return resp.Header, false, nil
	case http.StatusOK:
		return resp.Header, true, json.NewDecoder(resp.Body).Decode(into)
	}
	return resp.Header, false, fmt.Errorf("%s: %s", path, resp.Status)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
