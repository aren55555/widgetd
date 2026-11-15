// Package server is the weather widget's server half. It reads one place's
// forecast and air quality from Open-Meteo every quarter hour and snapshots
// the card when they change.
//
// It sends canonical units (°C, m/s, mm, Unix seconds) and the place's own
// time zone. Each screen converts to its units, and the card names days and
// times as they fall at the place.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/weather/gen/weather/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	// poll is how often it reads Open-Meteo, whose models update hourly.
	poll = 15 * time.Minute
	// retry is how soon it tries again after a failed read.
	retry = time.Minute
)

// Run is the widget's server half.
func Run(ctx context.Context, h widget.Host) error {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return err
	}
	// No title: the place's name is on the card itself, and "Weather" goes
	// without saying.
	card, err := widget.MountState[*pb.State](h, "forecast", widget.MountOptions{
		Layout: widget.Layout{Cols: 1, Rows: 1},
	})
	if err != nil {
		return err
	}
	if cfg.GetPlace() == "" {
		// Nothing to show until a place is picked; the card says so.
		if err := card.Snapshot(&pb.State{Temperature: cfg.GetTemperature()}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}

	var p *place
	var last *pb.State
	for {
		wait := poll
		if p == nil {
			p = resolve(ctx, h, cfg.GetPlace())
		}
		if p == nil {
			wait = retry
		} else if st, err := read(ctx, h, &cfg, *p); err != nil {
			// Keep showing what we had; try again soon.
			h.Logger().Warn("weather: read failed", "err", err)
			wait = retry
		} else if !proto.Equal(st, last) {
			if err := card.Snapshot(st); err != nil {
				return err
			}
			last = st
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// read builds the card from a fresh forecast. A missing AQI only leaves the
// AQI off the card.
func read(ctx context.Context, h widget.Host, cfg *pb.Config, p place) (*pb.State, error) {
	f, err := readForecast(ctx, h, p)
	if err != nil {
		return nil, err
	}
	aqi, err := readAQI(ctx, h, p)
	if err != nil {
		h.Logger().Warn("weather: no air quality", "err", err)
	}
	name := cfg.GetName()
	if name == "" {
		name = p.Name
	}
	return build(time.Now(), name, cfg.GetTemperature(), f, aqi), nil
}

// resolve finds the place by id, remembering it so that a geocoding outage
// at startup doesn't keep the card blank. Nil if it can't be found yet.
func resolve(ctx context.Context, h widget.Host, id string) *place {
	key := "place/" + id
	p, err := lookup(ctx, h, id)
	if err == nil {
		if b, err := json.Marshal(p); err == nil {
			if err := h.KV().Put(key, b); err != nil {
				h.Logger().Warn("weather: can't remember the place", "err", err)
			}
		}
		return &p
	}
	if errors.Is(err, errNoPlace) {
		h.Logger().Error("weather: Open-Meteo doesn't know this place; pick it again in settings", "place", id)
	} else {
		h.Logger().Warn("weather: place lookup failed", "err", err)
	}
	if b, ok, kvErr := h.KV().Get(key); kvErr == nil && ok {
		var cached place
		if json.Unmarshal(b, &cached) == nil {
			return &cached
		}
	}
	return nil
}
