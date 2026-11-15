// Package server is the bart-tracker widget's server half: one pair of BART
// stations. It keeps a digest of BART's published schedule (refreshed daily,
// cached in the instance's storage) and polls BART's realtime feed, and from
// the two it shows one card: the trains running between the stations either
// way, and the next direct trains from each to the other.
//
// Every time it sends is absolute; the view counts down and moves the trains
// from its own clock between polls.
package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/bart-tracker/gen/barttracker/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	feedURL     = "https://api.bart.gov/gtfsrt/tripupdate.aspx"
	scheduleURL = "https://www.bart.gov/dev/schedules/google_transit.zip"

	poll = 20 * time.Second
	// scheduleEvery is how old the schedule may get before it's fetched
	// again; scheduleRetry is the soonest it's fetched again when live trips
	// it doesn't know about suggest it's out of date.
	scheduleEvery = 24 * time.Hour
	scheduleRetry = time.Hour
	networkKey    = "network"
)

// Run is the widget's server half.
func Run(ctx context.Context, h widget.Host) error {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	t := &tracker{h: h, from: cfg.GetFrom(), to: cfg.GetTo()}
	net, err := t.network(ctx, false)
	if err != nil {
		// Nothing to go on: fail, so screens show why, and the host retries.
		return err
	}
	for _, id := range []string{t.from, t.to} {
		if stationIndex(net, id) < 0 {
			return fmt.Errorf("BART has no station %q", id)
		}
	}
	// No title: the card draws its own heading, the pair, and fills the tile.
	card, err := widget.MountState[*pb.State](h, "pair", widget.MountOptions{Layout: widget.Layout{Cols: 2, Rows: 2}})
	if err != nil {
		return err
	}

	log := h.Logger()
	var live []liveTrip
	var last *pb.State
	for {
		if l, err := t.feed(ctx); err != nil {
			log.Warn("bart-tracker: realtime feed failed; keeping the last", "err", err)
		} else {
			live = l
		}
		if n := t.refresh(ctx, net, live); n != nil {
			net = n
		}
		st := buildState(net, live, t.from, t.to, time.Now())
		if !sameBoard(st, last) {
			if err := card.Snapshot(st); err != nil {
				return err
			}
			last = st
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// sameBoard reports whether two states show the same, whenever made.
func sameBoard(a, b *pb.State) bool {
	if a == nil || b == nil {
		return a == b
	}
	a, b = proto.CloneOf(a), proto.CloneOf(b)
	a.AsOfUnixMs, b.AsOfUnixMs = 0, 0
	return proto.Equal(a, b)
}

type tracker struct {
	h        widget.Host
	from, to string
	// tried is when the schedule was last fetched, successfully or not.
	tried time.Time
}

// refresh fetches the schedule again if it's a day old, or sooner (but not
// more than hourly) if most live trips aren't in it: BART published a new
// one. It returns the new network, or nil to keep net.
func (t *tracker) refresh(ctx context.Context, net *pb.Network, live []liveTrip) *pb.Network {
	age := time.Since(time.UnixMilli(net.GetFetchedUnixMs()))
	unknown := 0
	for _, lt := range live {
		if _, ok := net.GetTrips()[lt.id]; !ok {
			unknown++
		}
	}
	stale := age > scheduleEvery || (unknown*2 > len(live) && age > scheduleRetry)
	if !stale || time.Since(t.tried) < scheduleRetry {
		return nil
	}
	n, err := t.network(ctx, true)
	if err != nil {
		t.h.Logger().Warn("bart-tracker: schedule refresh failed; keeping the last", "err", err)
		return nil
	}
	return n
}

// network is the schedule's digest: from storage if it's there and fresh
// isn't asked for, else from BART (and then stored).
func (t *tracker) network(ctx context.Context, fresh bool) (*pb.Network, error) {
	kv := t.h.KV()
	if !fresh {
		if b, ok, err := kv.Get(networkKey); err == nil && ok {
			var net pb.Network
			if proto.Unmarshal(b, &net) == nil && len(net.GetPatterns()) > 0 {
				return &net, nil
			}
		}
	}
	t.tried = time.Now()
	zipped, err := t.get(ctx, scheduleURL)
	if err != nil {
		return nil, fmt.Errorf("BART schedule: %w", err)
	}
	net, err := parseSchedule(zipped, time.Now())
	if err != nil {
		return nil, err
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(net)
	if err == nil {
		err = kv.Put(networkKey, b)
	}
	if err != nil {
		// Still usable; it's just fetched again on the next start.
		t.h.Logger().Warn("bart-tracker: couldn't store the schedule", "err", err)
	}
	return net, nil
}

// feed is the live trips from BART's realtime feed.
func (t *tracker) feed(ctx context.Context) ([]liveTrip, error) {
	b, err := t.get(ctx, feedURL)
	if err != nil {
		return nil, err
	}
	_, trips, err := parseFeed(b)
	return trips, err
}

func (t *tracker) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := t.h.Fetch(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return b, nil
}
