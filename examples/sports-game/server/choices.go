package server

import (
	"context"
	"sort"

	pb "github.com/aren55555/widgetd/examples/sports-game/gen/sportsgame/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Choices offers the chosen league's teams for the "team" field, so the
// settings form shows a list instead of asking for ESPN's abbreviation.
// Before a league is picked there's nothing to offer.
func Choices(ctx context.Context, h widget.Host) (map[string][]widget.Choice, error) {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return nil, err
	}
	if _, ok := leaguePath[cfg.GetLeague()]; !ok {
		return nil, nil
	}
	teams, err := espn{h: h, league: cfg.GetLeague()}.teams(ctx)
	if err != nil {
		return nil, err
	}
	return map[string][]widget.Choice{"team": teams}, nil
}

// teams is every team in the league, by name.
func (e espn) teams(ctx context.Context) ([]widget.Choice, error) {
	var resp struct {
		Sports []struct {
			Leagues []struct {
				Teams []struct {
					Team struct {
						Abbreviation string `json:"abbreviation"`
						DisplayName  string `json:"displayName"`
					} `json:"team"`
				} `json:"teams"`
			} `json:"leagues"`
		} `json:"sports"`
	}
	if err := e.get(ctx, "/teams", &resp); err != nil {
		return nil, err
	}
	var out []widget.Choice
	for _, s := range resp.Sports {
		for _, l := range s.Leagues {
			for _, t := range l.Teams {
				if t.Team.Abbreviation != "" {
					out = append(out, widget.Choice{Value: t.Team.Abbreviation, Label: t.Team.DisplayName})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}
