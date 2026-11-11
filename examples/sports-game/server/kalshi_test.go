package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/aren55555/widgetd/examples/sports-game/gen/sportsgame/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// fakeHost answers Fetch from canned bodies, keyed by a substring of the URL.
type fakeHost struct {
	widget.Host
	replies map[string]string
	asked   []string
}

func (f *fakeHost) Fetch(req *http.Request) (*http.Response, error) {
	u := req.URL.String()
	f.asked = append(f.asked, u)
	for k, body := range f.replies {
		if strings.Contains(u, k) {
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body))}, nil
		}
	}
	return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
}

func (f *fakeHost) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestKalshiFind(t *testing.T) {
	start := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	h := &fakeHost{replies: map[string]string{"/markets?": `{"markets":[
		{"ticker":"KXNHLGAME-26NOV09NYISJ-NYI","event_ticker":"KXNHLGAME-26NOV09NYISJ","occurrence_datetime":"2026-11-10T06:00:00Z"},
		{"ticker":"KXNHLGAME-26NOV09NYISJ-SJ","event_ticker":"KXNHLGAME-26NOV09NYISJ","occurrence_datetime":"2026-11-10T06:00:00Z"},
		{"ticker":"KXNHLGAME-26NOV09UTASJ-UTA","event_ticker":"KXNHLGAME-26NOV09UTASJ","occurrence_datetime":"2026-11-10T06:00:00Z"},
		{"ticker":"KXNHLGAME-26NOV09UTASJ-SJ","event_ticker":"KXNHLGAME-26NOV09UTASJ","occurrence_datetime":"2026-11-10T06:00:00Z"},
		{"ticker":"KXNHLGAME-26NOV11UTASJ-UTA","event_ticker":"KXNHLGAME-26NOV11UTASJ","occurrence_datetime":"2026-11-12T06:00:00Z"},
		{"ticker":"KXNHLGAME-26NOV11UTASJ-SJ","event_ticker":"KXNHLGAME-26NOV11UTASJ","occurrence_datetime":"2026-11-12T06:00:00Z"}
	]}`}}
	k := &kalshi{h: h, league: pb.League_LEAGUE_NHL}

	away, home, err := k.find(context.Background(), start, "NYI", "SJ")
	if err != nil || away != "KXNHLGAME-26NOV09NYISJ-NYI" || home != "KXNHLGAME-26NOV09NYISJ-SJ" {
		t.Fatalf("NYI @ SJ: %q %q %v", away, home, err)
	}
	// ESPN's UTAH is Kalshi's UTA; of two UTA @ SJ games, the one nearest the start.
	away, home, err = k.find(context.Background(), start, "UTAH", "SJ")
	if err != nil || away != "KXNHLGAME-26NOV09UTASJ-UTA" || home != "KXNHLGAME-26NOV09UTASJ-SJ" {
		t.Fatalf("UTAH @ SJ: %q %q %v", away, home, err)
	}
	if _, _, err := k.find(context.Background(), start, "SJ", "NYI"); err == nil {
		t.Fatal("home and away swapped should find nothing")
	}
	if q := h.asked[0]; !strings.Contains(q, "series_ticker=KXNHLGAME") || !strings.Contains(q, "min_close_ts=1794279600") {
		t.Fatalf("asked %s", q)
	}
}

// c is a minute that ended at end, with a trade at price ("" for none).
func c(end int64, price string) candle {
	k := candle{End: end}
	if price != "" {
		k.Price.Close = &price
	}
	return k
}

func TestMerge(t *testing.T) {
	const start = 1_000_000 * 60
	quotes := func(end int64, bid, ask string) candle {
		k := candle{End: end}
		k.Bid.Close, k.Ask.Close = &bid, &ask
		return k
	}
	got := merge(nil, start, []candle{
		c(start-120, "0.40"), // before the start: seeds minute 0
		c(start-60, "0.43"),
		c(start+60, "0.45"),
		c(start+120, ""),                  // no trade, no quotes: skipped
		quotes(start+240, "0.50", "0.52"), // no trade: the middle of the quotes
		c(start+300, "0.999"),             // rounds
	})
	// minute 0: 43 (the last before the start); 1: 45; 2, 3: held at 45; 4: 51; 5: 100.
	if want := []uint32{43, 45, 45, 45, 51, 100}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// A later fetch from just before the end replaces what it covers and extends.
	got = merge(got, start, []candle{c(start+240, "0.55"), c(start+300, "0.60"), c(start+360, "0.62")})
	if want := []uint32{43, 45, 45, 45, 55, 60, 62}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestKalshiMarket(t *testing.T) {
	start := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	h := &fakeHost{replies: map[string]string{
		"/markets?": `{"markets":[
			{"ticker":"KXNHLGAME-26NOV09NYISJ-NYI","event_ticker":"KXNHLGAME-26NOV09NYISJ"},
			{"ticker":"KXNHLGAME-26NOV09NYISJ-SJ","event_ticker":"KXNHLGAME-26NOV09NYISJ"}]}`,
		"NYISJ-NYI/candlesticks": `{"candlesticks":[{"end_period_ts":1794279600,"price":{"close_dollars":"0.57"}},{"end_period_ts":1794279660,"price":{"close_dollars":"0.60"}}]}`,
		"NYISJ-SJ/candlesticks":  `{"candlesticks":[{"end_period_ts":1794279600,"price":{"close_dollars":"0.43"}},{"end_period_ts":1794279660,"price":{"close_dollars":"0.40"}}]}`,
	}}
	k := &kalshi{h: h, league: pb.League_LEAGUE_NHL, games: map[string]*market{}}
	st := &pb.State{
		EventId: "401892682", StartUnixMs: start.UnixMilli(), Phase: pb.State_PHASE_PRE,
		Away: &pb.Team{Abbreviation: "NYI"}, Home: &pb.Team{Abbreviation: "SJ"},
	}
	now := start.Add(2 * time.Minute)
	if m := k.market(context.Background(), st, now); m != nil || len(h.asked) != 0 {
		t.Fatalf("before the start: %v, asked %v", m, h.asked)
	}
	st.Phase = pb.State_PHASE_LIVE
	m := k.market(context.Background(), st, now)
	if m == nil || m.GetStartUnixMs() != start.UnixMilli() || !slices.Equal(m.GetAway(), []uint32{57, 60}) || !slices.Equal(m.GetHome(), []uint32{43, 40}) {
		t.Fatalf("live: %v", m)
	}
	// Once final, one last fetch, then none.
	st.Phase = pb.State_PHASE_FINAL
	k.market(context.Background(), st, now)
	n := len(h.asked)
	if m := k.market(context.Background(), st, now); m == nil || len(h.asked) != n {
		t.Fatalf("settled: %v, asked %d more", m, len(h.asked)-n)
	}
}

func TestScored(t *testing.T) {
	got := scored([]play{
		{ScoringPlay: true, Wallclock: "2026-11-10T03:36:19Z"},
		{ScoringPlay: false, Wallclock: "2026-11-10T03:20:00Z"},
		{ScoringPlay: true, Wallclock: "2026-11-10T03:15:56Z"},
		{ScoringPlay: true, Wallclock: "2026-11-10T03:15:56Z"}, // the same play twice (plays and key events)
		{ScoringPlay: true, Wallclock: ""},
	})
	want := []int64{time.Date(2026, 11, 10, 3, 15, 56, 0, time.UTC).UnixMilli(), time.Date(2026, 11, 10, 3, 36, 19, 0, time.UTC).UnixMilli()}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
