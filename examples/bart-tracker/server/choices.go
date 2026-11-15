package server

import (
	"context"
	"sort"

	pb "github.com/aren55555/widgetd/examples/bart-tracker/gen/barttracker/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Choices offers BART's stations for both fields, by name, so the settings
// form shows lists instead of asking for station codes. Once the first
// station is picked, the second lists only the stations a train runs to
// directly from it.
func Choices(ctx context.Context, h widget.Host) (map[string][]widget.Choice, error) {
	var cfg pb.Config
	if err := h.Config(&cfg); err != nil {
		return nil, err
	}
	net, err := (&tracker{h: h}).network(ctx, false)
	if err != nil {
		return nil, err
	}
	return stationChoices(net, cfg.GetFrom()), nil
}

func stationChoices(net *pb.Network, from string) map[string][]widget.Choice {
	all := make([]widget.Choice, 0, len(net.GetStations()))
	for _, s := range net.GetStations() {
		all = append(all, widget.Choice{Value: s.GetId(), Label: s.GetName()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Label < all[j].Label })
	to := all
	if fi := stationIndex(net, from); fi >= 0 {
		direct := reachable(net, uint32(fi))
		to = nil
		for _, c := range all {
			if si := stationIndex(net, c.Value); si >= 0 && direct[uint32(si)] {
				to = append(to, c)
			}
		}
	}
	return map[string][]widget.Choice{"from": all, "to": to}
}
