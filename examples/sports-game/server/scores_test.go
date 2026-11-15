package server

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
)

// Two of the scoring plays of NYI @ SJ, Nov 9 2026, as ESPN's summary gives
// them (SJ, id 18, at home), plus the shootout winner (with ESPN's score
// after it, as hockey gives), a repeat of the first (soccer lists goals
// twice), a play that isn't a score, and a football play with no score given.
const nyiAtSJ = `[
 {"scoringPlay": true, "wallclock": "2026-11-10T03:36:19Z", "team": {"id": "18"},
  "text": "Ivar Stenberg Goal (6) Tip-In, assists: Tyler Toffoli (6), Ty Dellandrea (2)",
  "participants": [
   {"type": "scorer", "athlete": {"displayName": "Ivar Stenberg", "shortName": "I. Stenberg"}},
   {"type": "assister", "athlete": {"displayName": "Tyler Toffoli", "shortName": "T. Toffoli"}},
   {"type": "assister", "athlete": {"displayName": "Ty Dellandrea", "shortName": "T. Dellandrea"}}]},
 {"scoringPlay": true, "wallclock": "2026-11-10T03:15:56Z", "team": {"id": "12"},
  "text": "Ben Hutton Goal (1) Wrist Shot, assists: Simon Holmstrom (5), Bo Horvat (10)",
  "participants": [
   {"type": "scorer", "athlete": {"displayName": "Ben Hutton", "shortName": "B. Hutton"}},
   {"type": "assister", "athlete": {"displayName": "Simon Holmstrom", "shortName": "S. Holmstrom"}},
   {"type": "assister", "athlete": {"displayName": "Bo Horvat", "shortName": "B. Horvat"}}]},
 {"scoringPlay": true, "wallclock": "2026-11-10T03:15:56Z", "team": {"id": "12"}},
 {"scoringPlay": false, "wallclock": "2026-11-10T03:20:00Z", "team": {"id": "18"}},
 {"scoringPlay": true, "wallclock": "2026-11-10T05:58:34Z", "team": {"id": "18"},
  "awayScore": 1, "homeScore": 2, "text": "Ivar Stenberg Goal Wrist Shot",
  "participants": [{"type": "scorer", "athlete": {"displayName": "Ivar Stenberg", "shortName": "I. Stenberg"}}]},
 {"scoringPlay": true, "wallclock": "2026-11-10T06:30:00Z", "team": {"id": "18"},
  "text": "T.Bass 39 yard field goal is GOOD"}
]`

func TestScores(t *testing.T) {
	var plays []play
	if err := json.Unmarshal([]byte(nyiAtSJ), &plays); err != nil {
		t.Fatal(err)
	}
	got := scores(plays, "18")
	want := []string{
		// In time order, the repeat dropped; the away team's has home false (left out).
		`{"unixMs":"1794280556000","awayScore":1,"scorer":{"name":"Hutton","seasonTotal":1},"assists":[{"name":"Holmstrom","seasonTotal":5},{"name":"Horvat","seasonTotal":10}]}`,
		`{"unixMs":"1794281779000","home":true,"awayScore":1,"homeScore":1,"scorer":{"name":"Stenberg","seasonTotal":6},"assists":[{"name":"Toffoli","seasonTotal":6},{"name":"Dellandrea","seasonTotal":2}]}`,
		// A shootout goal: no season total.
		`{"unixMs":"1794290314000","home":true,"awayScore":1,"homeScore":2,"scorer":{"name":"Stenberg"}}`,
		// A play that names no one (football): just when, and whose.
		`{"unixMs":"1794292200000","home":true,"awayScore":1,"homeScore":3}`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d scores, want %d: %v", len(got), len(want), got)
	}
	for i, s := range got {
		b, err := protojson.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if g, w := canon(t, string(b)), canon(t, want[i]); g != w {
			t.Errorf("score %d:\n got %s\nwant %s", i, g, w)
		}
	}
}

// TestScoresCounted: soccer's key events give no score, so it's counted,
// and a shootout's kicks don't add to it.
func TestScoresCounted(t *testing.T) {
	var plays []play
	if err := json.Unmarshal([]byte(`[
 {"scoringPlay": true, "wallclock": "2026-10-26T01:46:34Z", "team": {"id": "1"}},
 {"scoringPlay": true, "wallclock": "2026-10-26T02:25:10Z", "team": {"id": "2"}},
 {"scoringPlay": true, "wallclock": "2026-10-26T02:40:00Z", "team": {"id": "2"}, "shootout": true}
]`), &plays); err != nil {
		t.Fatal(err)
	}
	got := scores(plays, "1")
	want := [][2]uint32{{0, 1}, {1, 1}, {1, 1}}
	if len(got) != len(want) {
		t.Fatalf("got %d scores, want %d", len(got), len(want))
	}
	for i, s := range got {
		if g := [2]uint32{s.GetAwayScore(), s.GetHomeScore()}; g != want[i] {
			t.Errorf("score %d: got %v, want %v", i, g, want[i])
		}
	}
}

// canon is JSON with its spacing and key order made the same, to compare.
func canon(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestLastName(t *testing.T) {
	cases := []struct{ full, short, want string }{
		{"Macklin Celebrini", "M. Celebrini", "Celebrini"},
		{"James van Riemsdyk", "J. van Riemsdyk", "van Riemsdyk"},
		{"Pelé", "", "Pelé"},
		{"Will Smith", "", "Smith"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := lastName(c.full, c.short); got != c.want {
			t.Errorf("lastName(%q, %q) = %q, want %q", c.full, c.short, got, c.want)
		}
	}
}
