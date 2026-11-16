// Package server is the portfolio-heatmap widget's server half. It reads
// the display's brokerage holdings through SnapTrade (read-only, signed by
// the host) every few minutes, and quotes for them every minute: Finnhub
// for stocks and funds, Coinbase for crypto, and once a day each holding's
// past month (see history.go). It sends each holding's weight in the
// portfolio and its percentage moves, today and over the past month, never a price, a share count or a dollar amount.
package server

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/portfolio-heatmap/gen/portfolio/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	// holdingsEvery is how often it rereads the holdings. SnapTrade caches
	// a brokerage's positions itself, so faster gains nothing.
	holdingsEvery = 5 * time.Minute
	// quotesEvery is the pause between rounds of quotes.
	quotesEvery = time.Minute
	// finnhubSpacing keeps Finnhub's free tier (60 calls a minute) clear:
	// with many holdings, a round of quotes takes longer than a minute.
	finnhubSpacing = 1100 * time.Millisecond
	// retry is how soon it tries the holdings again after a failure.
	retry = time.Minute
)

// Run is the widget's server half.
func Run(ctx context.Context, h widget.Host) error {
	card, err := widget.MountState[*pb.State](h, "heatmap", widget.MountOptions{
		Title:  "Portfolio",
		Layout: widget.Layout{Cols: 2, Rows: 2},
	})
	if err != nil {
		return err
	}

	var (
		positions []position
		asOf      time.Time
		readAt    time.Time
		quotes    = map[string]quote{}
		hist      = newHistory(h.KV())
		last      *pb.State
	)
	for {
		wait := quotesEvery
		if positions == nil || time.Since(readAt) >= holdingsEvery {
			p, err := readPositions(ctx, h)
			switch {
			case err != nil:
				h.Logger().Warn("portfolio-heatmap: can't read the holdings", "err", err)
				if positions == nil {
					wait = retry
				}
			default:
				positions, asOf, readAt = p, time.Now(), time.Now()
			}
		}
		if positions != nil {
			// Finnhub's calls are spaced out within the round, so a round
			// never asks for more than its free tier allows.
			readQuotes(ctx, h, positions, quotes, hist)
			st := build(asOf, positions, quotes, hist.closes, time.Now())
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

// readQuotes refreshes quotes for every held symbol, keeping the last good
// one when a read fails. Along the way, for the charts, it reads each
// symbol's month once a day (Yahoo for stocks, Coinbase for coins) and
// keeps each stock's previous close.
func readQuotes(ctx context.Context, h widget.Host, positions []position, quotes map[string]quote, hist *history) {
	seen := map[string]bool{}
	calls := 0
	for _, p := range positions {
		if seen[p.Symbol] || p.Symbol == "" || p.Units <= 0 {
			continue
		}
		seen[p.Symbol] = true
		if _, err := hist.series(p.Symbol); err != nil { // its stored closes, the first time
			h.Logger().Warn("portfolio-heatmap: can't read stored closes", "symbol", p.Symbol, "err", err)
		}
		var (
			q   quote
			ok  bool
			err error
		)
		switch {
		case p.Crypto:
			q, ok, err = coinbaseQuote(ctx, h, p.Symbol)
			if err == nil && ok {
				readMonth(ctx, h, hist, p.Symbol, coinbaseMonth)
			}
		default:
			readMonth(ctx, h, hist, p.Symbol, yahooMonth)
			if calls > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(finnhubSpacing):
				}
			}
			calls++
			q, ok, err = finnhubQuote(ctx, h, p.Symbol)
		}
		switch {
		case errors.Is(err, errRateLimited):
			h.Logger().Warn("portfolio-heatmap: quotes rate limited", "err", err)
			return
		case err != nil:
			h.Logger().Warn("portfolio-heatmap: quote failed", "symbol", p.Symbol, "err", err)
		case ok:
			quotes[p.Symbol] = q
			if err := hist.recordClose(p.Symbol, q.PrevDay, q.PrevClose); err != nil {
				h.Logger().Warn("portfolio-heatmap: can't keep a close", "symbol", p.Symbol, "err", err)
			}
		default:
			delete(quotes, p.Symbol)
		}
	}
	for s := range quotes {
		if !seen[s] {
			delete(quotes, s)
		}
	}
}

// readMonth reads symbol's month whole with read, if it's due.
func readMonth(ctx context.Context, h widget.Host, hist *history, symbol string,
	read func(context.Context, widget.Host, string, time.Time) ([]point, error),
) {
	now := time.Now()
	if !hist.monthDue(symbol, now) {
		return
	}
	pts, err := read(ctx, h, symbol, now)
	if err != nil {
		hist.monthFailed(symbol, now)
		h.Logger().Warn("portfolio-heatmap: can't read the month", "symbol", symbol, "err", err)
		return
	}
	hist.setMonth(symbol, now, pts)
}
