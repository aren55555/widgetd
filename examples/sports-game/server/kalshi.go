package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	pb "github.com/aren55555/widgetd/examples/sports-game/gen/sportsgame/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Kalshi's public trade API: read-only market data needs no key. This file
// is the only one that knows its shape. A game's market is an event with one
// binary market per team ("San Jose wins"), and soccer adds a tie.

const kalshiAPI = "https://api.elections.kalshi.com/trade-api/v2"

// kalshiSeries is each league's series of game-winner events.
var kalshiSeries = map[pb.League]string{
	pb.League_LEAGUE_NFL:  "KXNFLGAME",
	pb.League_LEAGUE_NHL:  "KXNHLGAME",
	pb.League_LEAGUE_NBA:  "KXNBAGAME",
	pb.League_LEAGUE_MLB:  "KXMLBGAME",
	pb.League_LEAGUE_WNBA: "KXWNBAGAME",
	pb.League_LEAGUE_MLS:  "KXMLSGAME",
}

// kalshiCode is Kalshi's code for a team where it isn't ESPN's abbreviation.
var kalshiCode = map[pb.League]map[string]string{
	pb.League_LEAGUE_NHL:  {"UTAH": "UTA"},
	pb.League_LEAGUE_NBA:  {"GS": "GSW", "NO": "NOP", "NY": "NYK", "SA": "SAS", "UTAH": "UTA", "WSH": "WAS"},
	pb.League_LEAGUE_NFL:  {"JAX": "JAC", "WSH": "WAS"},
	pb.League_LEAGUE_MLB:  {"ARI": "AZ", "CHW": "CWS"},
	pb.League_LEAGUE_WNBA: {"POR": "PDX"},
	pb.League_LEAGUE_MLS:  {"DC": "DCU", "LA": "LAG", "RBNY": "NYRB"},
}

const (
	// maxMinutes is Market's cap on prices per team (twelve hours).
	maxMinutes = 720
	// lookupRetry is how long to wait before looking again for a game
	// Kalshi had no market for.
	lookupRetry = 5 * time.Minute
)

type kalshi struct {
	h      widget.Host
	league pb.League
	games  map[string]*market
}

// market is what's known of one game's market.
type market struct {
	away, home string // the two teams' market tickers; empty if not found yet
	lookedAt   time.Time
	start      int64       // unix seconds of minute 0: the game's start
	prices     [2][]uint32 // away, home: a price per minute from start
	settled    bool        // fetched once after the game ended: nothing more to come
}

func (k *kalshi) code(team string) string {
	if c, ok := kalshiCode[k.league][team]; ok {
		return c
	}
	return team
}

// market is the game's market so far, or nil if there is none (yet). Errors
// are logged and leave the last known prices in place.
func (k *kalshi) market(ctx context.Context, st *pb.State, now time.Time) *pb.Market {
	if st.GetPhase() == pb.State_PHASE_PRE || kalshiSeries[k.league] == "" {
		return nil
	}
	id := st.GetEventId()
	m := k.games[id]
	if m == nil {
		m = &market{start: time.UnixMilli(st.GetStartUnixMs()).Truncate(time.Minute).Unix()}
		k.games[id] = m
	}
	log := k.h.Logger()
	if m.away == "" {
		if !m.lookedAt.IsZero() && now.Sub(m.lookedAt) < lookupRetry {
			return nil
		}
		m.lookedAt = now
		away, home, err := k.find(ctx, time.Unix(m.start, 0), st.GetAway().GetAbbreviation(), st.GetHome().GetAbbreviation())
		if err != nil {
			log.Warn("sports-game: no Kalshi market", "game", id, "err", err)
			return nil
		}
		m.away, m.home = away, home
	}
	if !m.settled {
		for side, ticker := range []string{m.away, m.home} {
			if err := k.update(ctx, m, side, ticker, now); err != nil {
				log.Warn("sports-game: Kalshi prices failed", "game", id, "market", ticker, "err", err)
			}
		}
		m.settled = st.GetPhase() == pb.State_PHASE_FINAL
	}
	n := min(len(m.prices[0]), len(m.prices[1]))
	if n == 0 {
		return nil
	}
	return &pb.Market{
		StartUnixMs: m.start * 1000,
		Away:        m.prices[0][:n],
		Home:        m.prices[1][:n],
	}
}

func (k *kalshi) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kalshiAPI+path, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "widgetd-sports-game")
	req.Header.Set("Accept", "application/json")
	resp, err := k.h.Fetch(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kalshi %s: %s", req.URL.Path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// find is the away and home teams' market tickers for the game starting at
// start. An event's ticker ends in the away team's code then the home team's
// ("KXNHLGAME-26NOV09NYISJ"), and each market's in its team's code. Markets
// close after the game ends, within a few days of the start.
func (k *kalshi) find(ctx context.Context, start time.Time, away, home string) (awayTicker, homeTicker string, err error) {
	a, h := k.code(away), k.code(home)
	q := url.Values{
		"series_ticker": {kalshiSeries[k.league]},
		"min_close_ts":  {strconv.FormatInt(start.Unix(), 10)},
		"max_close_ts":  {strconv.FormatInt(start.Add(4*24*time.Hour).Unix(), 10)},
		"limit":         {"1000"},
	}
	type found struct {
		away, home string
		gap        time.Duration // from the start to the market's expected time
	}
	events := map[string]*found{}
	for range 5 { // pages
		var resp struct {
			Cursor  string `json:"cursor"`
			Markets []struct {
				Ticker      string `json:"ticker"`
				EventTicker string `json:"event_ticker"`
				Occurrence  string `json:"occurrence_datetime"`
			} `json:"markets"`
		}
		if err := k.get(ctx, "/markets?"+q.Encode(), &resp); err != nil {
			return "", "", err
		}
		for _, m := range resp.Markets {
			if !strings.HasSuffix(m.EventTicker, a+h) {
				continue
			}
			f := events[m.EventTicker]
			if f == nil {
				f = &found{gap: math.MaxInt64}
				if t, err := time.Parse(time.RFC3339, m.Occurrence); err == nil {
					f.gap = t.Sub(start).Abs()
				}
				events[m.EventTicker] = f
			}
			switch m.Ticker[strings.LastIndexByte(m.Ticker, '-')+1:] {
			case a:
				f.away = m.Ticker
			case h:
				f.home = m.Ticker
			}
		}
		if resp.Cursor == "" {
			break
		}
		q.Set("cursor", resp.Cursor)
	}
	// Two events for the same teams in the window is a doubleheader: the one
	// expected nearest the start is this one.
	var best *found
	for _, f := range events {
		if f.away != "" && f.home != "" && (best == nil || f.gap < best.gap) {
			best = f
		}
	}
	if best == nil {
		return "", "", fmt.Errorf("no %s market for %s @ %s", kalshiSeries[k.league], away, home)
	}
	return best.away, best.home, nil
}

// update adds a team's prices up to now, from just before the last known
// minute (whose candle may have changed) or, the first time, from an hour
// before the start so minute 0 has a price.
func (k *kalshi) update(ctx context.Context, m *market, side int, ticker string, now time.Time) error {
	from := m.start - 3600
	if n := len(m.prices[side]); n > 1 {
		from = m.start + int64(n-2)*60
	}
	q := url.Values{
		"start_ts":        {strconv.FormatInt(from, 10)},
		"end_ts":          {strconv.FormatInt(now.Unix(), 10)},
		"period_interval": {"1"},
	}
	var resp struct {
		Candles []candle `json:"candlesticks"`
	}
	if err := k.get(ctx, "/series/"+url.PathEscape(kalshiSeries[k.league])+"/markets/"+url.PathEscape(ticker)+"/candlesticks?"+q.Encode(), &resp); err != nil {
		return err
	}
	m.prices[side] = merge(m.prices[side], m.start, resp.Candles)
	return nil
}

// candle is one minute of a market. Prices are dollars on a $1 contract,
// which read as the chance of Yes. A minute without a trade has no price.
type candle struct {
	End   int64 `json:"end_period_ts"`
	Price struct {
		Close *string `json:"close_dollars"`
	} `json:"price"`
	Bid struct {
		Close *string `json:"close_dollars"`
	} `json:"yes_bid"`
	Ask struct {
		Close *string `json:"close_dollars"`
	} `json:"yes_ask"`
}

// percent is the candle's price in whole percent: its last trade, or else
// the middle of its quotes.
func (c candle) percent() (uint32, bool) {
	dollars := func(s *string) (float64, bool) {
		if s == nil {
			return 0, false
		}
		f, err := strconv.ParseFloat(*s, 64)
		return f, err == nil
	}
	p, ok := dollars(c.Price.Close)
	if !ok {
		bid, ok1 := dollars(c.Bid.Close)
		ask, ok2 := dollars(c.Ask.Close)
		if !ok1 || !ok2 || ask <= 0 {
			return 0, false
		}
		p, ok = (bid+ask)/2, true
	}
	return uint32(min(100, max(0, math.Round(p*100)))), ok
}

// merge folds candles into a price per minute from start. Minute i is the
// price at start+i minutes: the last candle ending at or before then. Minutes
// with no price carry the last one forward; candles before start only seed
// minute 0. Earlier minutes are kept; a candle for a known minute replaces it.
func merge(prices []uint32, start int64, candles []candle) []uint32 {
	for _, c := range candles {
		p, ok := c.percent()
		if !ok {
			continue
		}
		i := int((c.End - start) / 60)
		if c.End < start {
			i = 0
		}
		if i >= maxMinutes {
			break
		}
		for len(prices) < i {
			// A gap: hold the last price, or this one if there's none yet.
			hold := p
			if len(prices) > 0 {
				hold = prices[len(prices)-1]
			}
			prices = append(prices, hold)
		}
		if i == len(prices) {
			prices = append(prices, p)
		} else {
			prices[i] = p
		}
	}
	return prices
}
