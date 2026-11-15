package server

import (
	"context"
	"errors"
	"strconv"
	"strings"

	pb "github.com/aren55555/widgetd/examples/weather/gen/weather/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Choices lists the places matching the search field for the place field,
// so a town is picked by name rather than typed in as an id. The form asks
// again as the search is edited.
func Choices(ctx context.Context, h widget.Host) (map[string][]widget.Choice, error) {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return nil, err
	}
	var out []widget.Choice
	seen := map[string]bool{}
	if q := strings.TrimSpace(cfg.GetSearch()); q != "" {
		found, err := search(ctx, h, q)
		if err != nil {
			return nil, err
		}
		for _, p := range found {
			v := strconv.FormatInt(p.ID, 10)
			out = append(out, widget.Choice{Value: v, Label: p.label()})
			seen[v] = true
		}
	}
	// Keep the saved place on the list, named, even once the search moves
	// on (or was never filled in).
	if id := cfg.GetPlace(); id != "" && !seen[id] {
		if p, err := lookup(ctx, h, id); err == nil {
			out = append([]widget.Choice{{Value: id, Label: p.label()}}, out...)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return map[string][]widget.Choice{"place": out}, nil
}

// Verify refuses settings without a place Open-Meteo knows.
func Verify(ctx context.Context, h widget.Host) (widget.Verdict, error) {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return widget.Verdict{}, err
	}
	if cfg.GetPlace() == "" {
		return widget.Verdict{Violations: []widget.Violation{{
			Field:   "place",
			Message: "Search for a town or city, then pick it from this list.",
		}}}, nil
	}
	if _, err := lookup(ctx, h, cfg.GetPlace()); errors.Is(err, errNoPlace) {
		return widget.Verdict{Violations: []widget.Violation{{
			Field:   "place",
			Message: "Open-Meteo doesn't know this place any more. Search for it again.",
		}}}, nil
	} else if err != nil {
		return widget.Verdict{}, err
	}
	return widget.Verdict{}, nil
}
