package server

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	pb "github.com/aren55555/widgetd/examples/sports-game/gen/sportsgame/v1"
)

// The scoring plays of ESPN's play-by-play, as marks for the chart: when,
// which team, and (where ESPN names them, as for hockey) who scored and
// assisted, with their season totals.

// maxScores is State's cap on scores.
const maxScores = 40

type teamRef struct {
	ID string `json:"id"`
}

type play struct {
	ScoringPlay  bool    `json:"scoringPlay"`
	Wallclock    string  `json:"wallclock"`
	Team         teamRef `json:"team"`
	Text         string  `json:"text"`
	Participants []struct {
		Type    string `json:"type"` // scorer, assister
		Athlete struct {
			DisplayName string `json:"displayName"` // "Macklin Celebrini"
			ShortName   string `json:"shortName"`   // "M. Celebrini"
		} `json:"athlete"`
	} `json:"participants"`
}

// scores is each scoring play, in order, once each (soccer lists goals in
// both its plays and key events), at most maxScores. homeID is ESPN's id for
// the home team.
func scores(plays []play, homeID string) []*pb.Score {
	seen := map[int64]bool{}
	var out []*pb.Score
	for _, p := range plays {
		if !p.ScoringPlay {
			continue
		}
		t, err := time.Parse(time.RFC3339, p.Wallclock)
		if err != nil || seen[t.UnixMilli()] {
			continue
		}
		seen[t.UnixMilli()] = true
		s := &pb.Score{UnixMs: t.UnixMilli(), Home: p.Team.ID != "" && p.Team.ID == homeID}
		for _, who := range p.Participants {
			c := credit(p.Text, who.Athlete.DisplayName, who.Athlete.ShortName)
			if c == nil {
				continue
			}
			switch {
			case who.Type == "scorer" && s.Scorer == nil:
				s.Scorer = c
			case who.Type == "assister" && len(s.Assists) < 3:
				s.Assists = append(s.Assists, c)
			}
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *pb.Score) int { return cmp.Compare(a.GetUnixMs(), b.GetUnixMs()) })
	if len(out) > maxScores {
		out = out[:maxScores]
	}
	return out
}

// credit is a player's part in a play: their last name, and the season
// total ESPN writes after their name in the play's text, as in
// "Ben Hutton Goal (1) Wrist Shot, assists: Simon Holmstrom (5), Bo Horvat (10)".
// A shootout goal has no total. Nil without a name.
func credit(text, fullName, shortName string) *pb.Credit {
	name := lastName(fullName, shortName)
	if name == "" {
		return nil
	}
	c := &pb.Credit{Name: name}
	if fullName != "" {
		re := regexp.MustCompile(regexp.QuoteMeta(fullName) + `(?: Goal)? \((\d{1,3})\)`)
		if m := re.FindStringSubmatch(text); m != nil {
			n, _ := strconv.ParseUint(m[1], 10, 32)
			c.SeasonTotal = uint32(n)
		}
	}
	return c
}

// lastName is "Celebrini" from ESPN's "M. Celebrini" (which keeps a
// many-word surname whole: "J. van Riemsdyk"), or else the full name's last
// word. At most 48 bytes, Credit's cap.
func lastName(fullName, shortName string) string {
	name := shortName
	if i := strings.Index(name, ". "); i >= 0 && i < 4 {
		name = name[i+2:]
	} else if f := strings.Fields(fullName); len(f) > 0 {
		name = f[len(f)-1]
	}
	return clip(strings.TrimSpace(name), 48)
}
