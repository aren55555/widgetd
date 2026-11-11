// Package server is the bitcoin-spiral widget's server half. It keeps a daily
// close history (backfilled once, then extended each day) in the instance's
// storage, polls the live price, and drives one card: a snapshot of the whole
// history once, then a small delta per price change or closed day.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/bitcoin-spiral/gen/bitcoinspiral/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	api = "https://api.exchange.coinbase.com"
	// pagePause spaces out history pages: a little over a second, so a first
	// backfill (dozens of pages) stays inside the host's default allowance of
	// 60 requests a minute and leaves room for the live price.
	pagePause = 1100 * time.Millisecond
	userAgent = "widgetd-bitcoin-spiral"
	// pollEvery is how often the live price is read. Coinbase allows far
	// more; a few seconds is "live" for a chart drawn on a log scale.
	pollEvery = 5 * time.Second
	// candlesPerRequest is Coinbase's page size for daily candles.
	candlesPerRequest = 300
	day               = 24 * time.Hour
)

// firstDay is where each product's daily history starts on Coinbase.
var firstDay = map[pb.Currency]time.Time{
	pb.Currency_CURRENCY_USD: time.Date(2015, 7, 20, 0, 0, 0, 0, time.UTC),
	pb.Currency_CURRENCY_EUR: time.Date(2015, 4, 23, 0, 0, 0, 0, time.UTC),
	pb.Currency_CURRENCY_GBP: time.Date(2015, 4, 21, 0, 0, 0, 0, time.UTC),
}

var product = map[pb.Currency]string{
	pb.Currency_CURRENCY_USD: "BTC-USD",
	pb.Currency_CURRENCY_EUR: "BTC-EUR",
	pb.Currency_CURRENCY_GBP: "BTC-GBP",
}

var symbol = map[pb.Currency]string{
	pb.Currency_CURRENCY_USD: "USD",
	pb.Currency_CURRENCY_EUR: "EUR",
	pb.Currency_CURRENCY_GBP: "GBP",
}

// Run is the widget's server half.
func Run(ctx context.Context, h widget.Host) error {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	cur := cfg.GetCurrency()
	if cur == pb.Currency_CURRENCY_UNSPECIFIED {
		cur = pb.Currency_CURRENCY_USD
	}
	w := &spiral{h: h, cur: cur, older: cfg.GetOlderHistory(), state: &pb.State{Currency: cur}}
	return w.run(ctx)
}

type spiral struct {
	h     widget.Host
	cur   pb.Currency
	older pb.OlderHistory
	state *pb.State
	card  *widget.Card[*pb.State, *pb.Delta]
	// olderN is how many of state.Daily came from the older source: they
	// lead the slice, and are stored apart from the Coinbase days.
	olderN int
}

func (w *spiral) historyKey() string { return "history:" + symbol[w.cur] }

func (w *spiral) run(ctx context.Context) error {
	log := w.h.Logger()
	if err := w.loadHistory(); err != nil {
		log.Warn("bitcoin-spiral: stored history unreadable; refetching", "err", err)
	}
	card, err := widget.Mount[*pb.State, *pb.Delta](w.h, "spiral", widget.MountOptions{
		Title:  "BTC / " + symbol[w.cur],
		Layout: widget.Layout{Cols: 2, Rows: 2},
	})
	if err != nil {
		return err
	}
	w.card = card

	// Show what's stored straight away; backfill whatever's missing after.
	if err := card.Snapshot(w.state); err != nil {
		return err
	}
	if added, err := w.backfill(ctx); err != nil {
		log.Warn("bitcoin-spiral: backfill incomplete; will retry", "err", err)
	} else if len(added) > 0 {
		if err := card.Snapshot(w.state); err != nil {
			return err
		}
	}
	if n, err := w.loadOlder(ctx); err != nil {
		log.Warn("bitcoin-spiral: older history unavailable", "source", w.older, "err", err)
	} else if n > 0 {
		if err := card.Snapshot(w.state); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	lastDay := today()
	for {
		if err := w.poll(ctx); err != nil && ctx.Err() == nil {
			log.Warn("bitcoin-spiral: price poll failed", "err", err)
		}
		// A new UTC day: yesterday's candle is final now.
		if d := today(); d != lastDay {
			added, err := w.backfill(ctx)
			switch {
			case err != nil:
				log.Warn("bitcoin-spiral: daily close fetch failed", "err", err)
			case len(added) == 1: // the usual case: one day closed
				err = card.Delta(&pb.Delta{Change: &pb.Delta_Close{Close: added[0]}}, w.current)
			case len(added) > 1: // back after a gap: resend the lot
				err = card.Snapshot(w.state)
			}
			if err == nil {
				lastDay = d
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func today() int32 { return int32(time.Now().UTC().Unix() / int64(day/time.Second)) }

func (w *spiral) loadHistory() error {
	b, ok, err := w.h.KV().Get(w.historyKey())
	if err != nil || !ok {
		return err
	}
	var hist pb.History
	if err := proto.Unmarshal(b, &hist); err != nil {
		return err
	}
	w.state.Daily = hist.GetDaily()
	return nil
}

func (w *spiral) saveHistory() error {
	b, err := proto.Marshal(&pb.History{Daily: w.state.GetDaily()[w.olderN:]})
	if err != nil {
		return err
	}
	return w.h.KV().Put(w.historyKey(), b)
}

// backfill fetches every closed day after the last one held, up to
// yesterday, a page at a time, stores them, and returns what it added. The
// caller decides how to tell screens: a delta for one day, a snapshot for
// many.
func (w *spiral) backfill(ctx context.Context) ([]*pb.DailyClose, error) {
	from := firstDay[w.cur]
	if n := len(w.state.Daily); n > 0 {
		from = time.Unix(int64(w.state.Daily[n-1].GetDay()+1)*int64(day/time.Second), 0).UTC()
	}
	end := time.Unix(int64(today())*int64(day/time.Second), 0).UTC() // exclusive: today hasn't closed
	var added []*pb.DailyClose
	defer func() {
		if len(added) > 0 {
			if err := w.saveHistory(); err != nil {
				w.h.Logger().Warn("bitcoin-spiral: saving history failed", "err", err)
			}
		}
	}()
	for from.Before(end) {
		to := from.Add(candlesPerRequest * day)
		if to.After(end) {
			to = end
		}
		closes, err := w.candles(ctx, from, to)
		if err != nil {
			return added, err
		}
		for _, c := range closes {
			if n := len(w.state.Daily); n > 0 && c.GetDay() <= w.state.Daily[n-1].GetDay() {
				continue
			}
			w.state.Daily = append(w.state.Daily, c)
			added = append(added, c)
		}
		from = to
		if from.Before(end) {
			select {
			case <-ctx.Done():
				return added, ctx.Err()
			case <-time.After(pagePause):
			}
		}
	}
	return added, nil
}

func (w *spiral) current() *pb.State { return w.state }

// candles returns daily closes in [from, to), oldest first.
func (w *spiral) candles(ctx context.Context, from, to time.Time) ([]*pb.DailyClose, error) {
	q := url.Values{
		"granularity": {"86400"},
		"start":       {from.Format(time.RFC3339)},
		"end":         {to.Add(-time.Second).Format(time.RFC3339)},
	}
	var rows [][]float64 // [time, low, high, open, close, volume]
	if err := w.get(ctx, "/products/"+product[w.cur]+"/candles?"+q.Encode(), &rows); err != nil {
		return nil, err
	}
	var out []*pb.DailyClose
	for _, r := range rows {
		if len(r) < 5 || r[4] <= 0 {
			continue
		}
		secs := int64(day / time.Second)
		d := int64(r[0]) / secs
		if d < from.Unix()/secs || d >= to.Unix()/secs {
			continue
		}
		out = append(out, &pb.DailyClose{Day: int32(d), Close: r[4]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetDay() < out[j].GetDay() })
	return out, nil
}

func (w *spiral) poll(ctx context.Context) error {
	var t struct {
		Price string    `json:"price"`
		Time  time.Time `json:"time"`
	}
	if err := w.get(ctx, "/products/"+product[w.cur]+"/ticker", &t); err != nil {
		return err
	}
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || price <= 0 {
		return fmt.Errorf("ticker price %q", t.Price)
	}
	if l := w.state.GetLatest(); l != nil && l.GetPrice() == price {
		return nil
	}
	q := &pb.Quote{UnixMs: t.Time.UnixMilli(), Price: price}
	if q.UnixMs <= 0 {
		q.UnixMs = time.Now().UnixMilli()
	}
	w.state.Latest = q
	return w.card.Delta(&pb.Delta{Change: &pb.Delta_Quote{Quote: q}}, w.current)
}

// get reads a Coinbase API path as JSON.
func (w *spiral) get(ctx context.Context, path string, into any) error {
	return w.getJSON(ctx, api+path, into)
}

// getJSON reads a URL as JSON, through the host's policed fetch.
func (w *spiral) getJSON(ctx context.Context, rawURL string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := w.h.Fetch(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", req.URL.Host+req.URL.Path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
