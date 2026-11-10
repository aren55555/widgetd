// Package server is the calendar widget's server half. It reads the bound
// Google account's calendars for a window around the coming fortnight,
// merges them into one timeline, and snapshots the card when anything
// changes. It never sees the account's token: requests it marks with
// widget.Authorize are signed by the host.
//
// Everything it sends is in UTC. Each screen cuts the timeline into days in
// its own time zone, so the server has no zone or midnight logic at all.
package server

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/calendar/gen/calendar/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// poll is how often it asks Google. Each ask re-reads the window, a few KB.
// (Google's push notifications would let this be much rarer; they need a
// public webhook, which the host doesn't offer yet.)
const poll = time.Minute

// Run is the widget's server half.
func Run(ctx context.Context, h widget.Host) error {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	s := resolve(&cfg)
	// No title: the fortnight fills the whole card.
	card, err := widget.MountState[*pb.State](h, "fortnight", widget.MountOptions{
		Layout: widget.Layout{Cols: 4, Rows: 2},
	})
	if err != nil {
		return err
	}
	fields := fieldsFull
	if s.detail == pb.Detail_DETAIL_BUSY {
		fields = fieldsBusy
	}

	var last *pb.State
	for {
		now := time.Now()
		from, to := window(now)
		cals, err := readAll(ctx, h, s.calendars, from, to, fields)
		var scope *widget.ScopeError
		switch {
		case errors.As(err, &scope):
			return err // only a new build can fix it
		case err != nil:
			// Keep showing what we had; try again next time.
			h.Logger().Warn("calendar: read failed", "err", err)
		default:
			st := build(now, s, cals)
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
		case <-time.After(poll):
		}
	}
}

// readAll reads every calendar. One failing fails the lot, so a calendar
// that's briefly unreachable doesn't make its events vanish from the card;
// one that's gone for good (deleted, or no longer shared) is skipped.
func readAll(ctx context.Context, h widget.Host, calendars []string, from, to time.Time, fields string) ([][]gEvent, error) {
	out := make([][]gEvent, 0, len(calendars))
	for _, c := range calendars {
		evs, err := events(ctx, h, c, from, to, fields)
		if gone(err) {
			h.Logger().Warn("calendar: skipping a calendar Google can't find", "calendar", c, "err", err)
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, evs)
	}
	return out, nil
}
