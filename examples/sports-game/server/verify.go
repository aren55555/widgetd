package server

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/aren55555/widgetd/examples/sports-game/gen/sportsgame/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Verify checks that the team exists in the chosen league before the
// settings are saved, so a mistyped team is caught on the phone rather than
// showing up later as a stopped card. It saves ESPN's spelling ("sj" → "SJ").
func Verify(ctx context.Context, h widget.Host) (widget.Verdict, error) {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return widget.Verdict{}, err
	}
	teams, err := espn{h: h, league: cfg.GetLeague()}.teams(ctx)
	if err != nil {
		return widget.Verdict{}, err
	}
	want := strings.ToUpper(cfg.GetTeam())
	for _, t := range teams {
		if t.Value == want {
			cfg.Team = t.Value
			return widget.Verdict{Config: &cfg}, nil
		}
	}
	league := strings.TrimPrefix(cfg.GetLeague().String(), "LEAGUE_")
	msg := fmt.Sprintf("ESPN has no %s team %q.", league, cfg.GetTeam())
	if s := suggest(want, teams); len(s) > 0 {
		msg += " Did you mean " + strings.Join(s, " or ") + "?"
	}
	return widget.Verdict{Violations: []widget.Violation{{Field: "team", Message: msg}}}, nil
}

// suggest names up to two teams whose abbreviation is close to want: one
// starts the other ("SJS" and "SJ"), or they share their first two letters.
func suggest(want string, teams []widget.Choice) []string {
	var out []string
	for _, t := range teams {
		close := strings.HasPrefix(want, t.Value) || strings.HasPrefix(t.Value, want) ||
			(len(want) >= 2 && len(t.Value) >= 2 && want[:2] == t.Value[:2])
		if close && len(out) < 2 {
			out = append(out, fmt.Sprintf("%s (%s)", t.Value, t.Label))
		}
	}
	return out
}
