package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	pb "github.com/aren55555/widgetd/examples/sports-game/gen/sportsgame/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// ESPN's public site API: unauthenticated, unversioned, and unofficial, so
// this file is the only one that knows its shape.

const espnAPI = "https://site.api.espn.com/apis/site/v2/sports/"

// leaguePath is each league's path in the API.
var leaguePath = map[pb.League]string{
	pb.League_LEAGUE_NFL:  "football/nfl",
	pb.League_LEAGUE_NHL:  "hockey/nhl",
	pb.League_LEAGUE_NBA:  "basketball/nba",
	pb.League_LEAGUE_MLB:  "baseball/mlb",
	pb.League_LEAGUE_WNBA: "basketball/wnba",
	pb.League_LEAGUE_MLS:  "soccer/usa.1",
}

// typicalLength is a generous game length, for when the server half didn't
// see a game end (it was stopped at the time) and must guess when it did.
var typicalLength = map[pb.League]time.Duration{
	pb.League_LEAGUE_NFL:  4 * time.Hour,
	pb.League_LEAGUE_NHL:  3 * time.Hour,
	pb.League_LEAGUE_NBA:  3 * time.Hour,
	pb.League_LEAGUE_MLB:  4 * time.Hour,
	pb.League_LEAGUE_WNBA: 3 * time.Hour,
	pb.League_LEAGUE_MLS:  2 * time.Hour,
}

// scheduled is one game from a team's schedule.
type scheduled struct {
	id        string
	start     time.Time
	completed bool
}

type espn struct {
	h      widget.Host
	league pb.League
}

func (e espn) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, espnAPI+leaguePath[e.league]+path, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "widgetd-sports-game")
	req.Header.Set("Accept", "application/json")
	resp, err := e.h.Fetch(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("espn %s: %s", req.URL.Path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

var errNotFound = errors.New("not found")

// schedule is a team's season: every game, past and to come.
func (e espn) schedule(ctx context.Context, team string) (string, []scheduled, error) {
	var resp struct {
		Team struct {
			DisplayName string `json:"displayName"`
		} `json:"team"`
		Events []struct {
			ID           string `json:"id"`
			Date         string `json:"date"`
			Competitions []struct {
				Status struct {
					Type struct {
						Completed bool `json:"completed"`
					} `json:"type"`
				} `json:"status"`
			} `json:"competitions"`
		} `json:"events"`
	}
	err := e.get(ctx, "/teams/"+url.PathEscape(strings.ToLower(team))+"/schedule", &resp)
	if errors.Is(err, errNotFound) || (err == nil && resp.Team.DisplayName == "") {
		return "", nil, fmt.Errorf("ESPN has no %s team %q", pb.League_name[int32(e.league)][len("LEAGUE_"):], team)
	}
	if err != nil {
		return "", nil, err
	}
	var out []scheduled
	for _, ev := range resp.Events {
		start, err := parseESPNTime(ev.Date)
		if err != nil {
			continue
		}
		s := scheduled{id: ev.ID, start: start}
		if len(ev.Competitions) > 0 {
			s.completed = ev.Competitions[0].Status.Type.Completed
		}
		out = append(out, s)
	}
	return resp.Team.DisplayName, out, nil
}

// game is one game, as its card shows it.
func (e espn) game(ctx context.Context, id string) (*pb.State, error) {
	var resp struct {
		Header struct {
			Competitions []struct {
				Date   string `json:"date"`
				Status struct {
					Type struct {
						State       string `json:"state"` // pre, in, post
						ShortDetail string `json:"shortDetail"`
					} `json:"type"`
				} `json:"status"`
				Competitors []struct {
					HomeAway string `json:"homeAway"`
					Score    string `json:"score"`
					Winner   bool   `json:"winner"`
					Team     struct {
						Abbreviation string `json:"abbreviation"`
						DisplayName  string `json:"displayName"`
						ShortName    string `json:"shortDisplayName"`
						Color        string `json:"color"`
						Logos        []struct {
							Href string `json:"href"`
						} `json:"logos"`
					} `json:"team"`
					Record []struct {
						Summary string `json:"summary"`
					} `json:"record"`
				} `json:"competitors"`
				Broadcasts []struct {
					Media struct {
						ShortName string `json:"shortName"`
					} `json:"media"`
				} `json:"broadcasts"`
			} `json:"competitions"`
		} `json:"header"`
	}
	if err := e.get(ctx, "/summary?event="+url.QueryEscape(id), &resp); err != nil {
		return nil, err
	}
	if len(resp.Header.Competitions) == 0 {
		return nil, fmt.Errorf("game %s: no competition", id)
	}
	c := resp.Header.Competitions[0]
	start, err := parseESPNTime(c.Date)
	if err != nil {
		return nil, fmt.Errorf("game %s: %w", id, err)
	}
	st := &pb.State{
		League:      e.league,
		EventId:     id,
		StartUnixMs: start.UnixMilli(),
		Detail:      clip(c.Status.Type.ShortDetail, 64),
		Phase: map[string]pb.State_Phase{
			"pre": pb.State_PHASE_PRE, "in": pb.State_PHASE_LIVE, "post": pb.State_PHASE_FINAL,
		}[c.Status.Type.State],
	}
	if len(c.Broadcasts) > 0 {
		st.Broadcast = clip(c.Broadcasts[0].Media.ShortName, 48)
	}
	for _, comp := range c.Competitors {
		t := &pb.Team{
			Abbreviation: comp.Team.Abbreviation,
			Name:         clip(comp.Team.DisplayName, 64),
			ShortName:    clip(comp.Team.ShortName, 32),
			Score:        clip(comp.Score, 8),
			Color:        comp.Team.Color,
			Winner:       comp.Winner,
		}
		if len(comp.Team.Logos) > 0 && strings.HasPrefix(comp.Team.Logos[0].Href, "https://a.espncdn.com/") {
			t.Logo = comp.Team.Logos[0].Href
		}
		if len(comp.Record) > 0 {
			t.Record = clip(comp.Record[0].Summary, 16)
		}
		if comp.HomeAway == "home" {
			st.Home = t
		} else {
			st.Away = t
		}
	}
	if st.Home == nil || st.Away == nil {
		return nil, fmt.Errorf("game %s: missing a team", id)
	}
	return st, nil
}

// ESPN writes times as "2026-11-08T18:00Z": RFC 3339 without seconds.
func parseESPNTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04Z07:00", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable time %q", s)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
