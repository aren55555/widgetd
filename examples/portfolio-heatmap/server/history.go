package server

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

// History for the chart on an opened tile: a month of daily closes per
// holding. Free market data has no stock history, so the widget gathers
// its own: every Finnhub quote carries the previous trading day's close,
// which is kept in the instance's KV, one a day. A stock's chart fills in
// over its first month. Crypto needs none of that: Coinbase's public daily
// candles give the whole month at once.

const (
	// monthDays is how far back a chart goes.
	monthDays = 30
	// keepCloses is how many closes a stock keeps: a month of trading days
	// and then some.
	keepCloses = 40
	dayLayout  = "2006-01-02"
)

// point is a price at the end of a day ("2006-01-02", UTC for crypto,
// the trading day for stocks). It never leaves the server.
type point struct {
	Day   string  `json:"d"`
	Price float64 `json:"p"`
}

// history holds the closes the widget knows, by symbol.
type history struct {
	kv     widget.KV
	closes map[string][]point
	loaded map[string]bool
	// candlesOn is the UTC day each coin's candles were last read.
	candlesOn map[string]string
}

func newHistory(kv widget.KV) *history {
	return &history{kv: kv, closes: map[string][]point{}, loaded: map[string]bool{}, candlesOn: map[string]string{}}
}

func closesKey(symbol string) string { return "closes/" + symbol }

// series is the closes known for symbol, oldest first, reading its stored
// ones the first time it is asked for.
func (hs *history) series(symbol string) ([]point, error) {
	if !hs.loaded[symbol] && hs.kv != nil {
		hs.loaded[symbol] = true
		b, ok, err := hs.kv.Get(closesKey(symbol))
		if err != nil {
			return nil, fmt.Errorf("reading %s's closes: %w", symbol, err)
		}
		if ok {
			var pts []point
			if err := json.Unmarshal(b, &pts); err != nil {
				return nil, fmt.Errorf("reading %s's closes: %w", symbol, err)
			}
			hs.closes[symbol] = pts
		}
	}
	return hs.closes[symbol], nil
}

// recordClose keeps a stock's close for day, and stores its closes if
// that changed them.
func (hs *history) recordClose(symbol, day string, price float64) error {
	if !(price > 0) || math.IsInf(price, 0) || day == "" {
		return nil
	}
	pts, err := hs.series(symbol)
	if err != nil {
		return err
	}
	i, found := slices.BinarySearchFunc(pts, day, func(p point, d string) int {
		switch {
		case p.Day < d:
			return -1
		case p.Day > d:
			return 1
		}
		return 0
	})
	switch {
	case found && pts[i].Price == price:
		return nil
	case found:
		pts[i].Price = price
	case len(pts) > 0 && i == len(pts) && pts[i-1].Price == price:
		// The same close again under a later day: the day after a market
		// holiday, whose previous close is the one before the holiday.
		return nil
	default:
		pts = slices.Insert(pts, i, point{Day: day, Price: price})
	}
	if len(pts) > keepCloses {
		pts = pts[len(pts)-keepCloses:]
	}
	hs.closes[symbol] = pts
	if hs.kv == nil {
		return nil
	}
	b, err := json.Marshal(pts)
	if err != nil {
		return err
	}
	if err := hs.kv.Put(closesKey(symbol), b); err != nil {
		return fmt.Errorf("storing %s's closes: %w", symbol, err)
	}
	return nil
}

// setCandles replaces a coin's closes with ones read from its candles.
// They aren't stored: they can be read again whenever they're wanted.
func (hs *history) setCandles(symbol, today string, pts []point) {
	hs.loaded[symbol] = true
	hs.closes[symbol] = pts
	hs.candlesOn[symbol] = today
}

// tradingDay is the US market day a Finnhub quote's time falls on. Every
// session hour (04:00 to 20:00 Eastern) is on the same calendar day at
// UTC-5, winter or summer, so no time zone database is needed.
func tradingDay(t time.Time) time.Time {
	d := t.UTC().Add(-5 * time.Hour)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

// dayBefore is the weekday before day. (A market holiday isn't known
// here; recordClose drops the repeated close that one causes.)
func dayBefore(day time.Time) time.Time {
	d := day.AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// monthPct is the chart for a holding: its closes over the month to
// today, then live (the latest price, on liveDay, replacing that day's
// close if there is one), each as percent change from the first. Nil
// with fewer than two points. from is the first point's day.
func monthPct(pts []point, live float64, liveDay string, today time.Time) (pcts []float32, from time.Time) {
	cutoff := today.AddDate(0, 0, -monthDays).Format(dayLayout)
	var use []point
	for _, p := range pts {
		if p.Day >= cutoff && p.Price > 0 {
			use = append(use, p)
		}
	}
	if live > 0 && liveDay != "" {
		switch n := len(use); {
		case n > 0 && use[n-1].Day == liveDay:
			use[n-1].Price = live
		case n == 0 || use[n-1].Day < liveDay:
			use = append(use, point{Day: liveDay, Price: live})
		}
	}
	if len(use) < 2 {
		return nil, time.Time{}
	}
	if len(use) > keepCloses {
		use = use[len(use)-keepCloses:]
	}
	from, err := time.Parse(dayLayout, use[0].Day)
	if err != nil {
		return nil, time.Time{}
	}
	base := use[0].Price
	for _, p := range use {
		v := pct((p.Price/base-1)*100, 100000)
		if v == nil {
			return nil, time.Time{}
		}
		pcts = append(pcts, float32(*v))
	}
	return pcts, from
}
