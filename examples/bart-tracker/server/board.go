package server

import (
	"slices"
	"sort"
	"time"

	pb "github.com/aren55555/widgetd/examples/bart-tracker/gen/barttracker/v1"
)

const (
	// keepDepartures is how many departures each way the state carries: the
	// view shows three, and the rest stand by as trains leave between states.
	keepDepartures = 5
	// leftGrace is how long a train stays listed after it leaves, as "0".
	leftGrace = 30 * time.Second
)

// buildState is the card for the pair from → to (station ids), from the
// schedule's network and the live trips, at now. A train counts only if its
// pattern stops at both stations, in the order it's going.
func buildState(net *pb.Network, live []liveTrip, from, to string, now time.Time) *pb.State {
	st := &pb.State{AsOfUnixMs: now.UnixMilli()}
	fi, ti := stationIndex(net, from), stationIndex(net, to)
	if fi < 0 || ti < 0 {
		return st
	}
	st.From, st.To = net.GetStations()[fi], net.GetStations()[ti]
	st.Line = lineBetween(net, uint32(fi), uint32(ti))
	if st.Line == nil {
		return st
	}
	onLine := map[string]uint32{}
	for i, s := range st.GetLine() {
		onLine[s.GetId()] = uint32(i)
	}

	for _, lt := range live {
		pi, ok := net.GetTrips()[lt.id]
		if !ok {
			continue
		}
		p := net.GetPatterns()[pi]
		// The stations it has still to make, alongside their stops.
		var rem []uint32
		var stops []liveStop
		for _, s := range lt.stops {
			if si, ok := net.GetPlatforms()[s.platform]; ok {
				rem = append(rem, si)
				stops = append(stops, s)
			}
		}
		if len(rem) == 0 {
			continue
		}
		for _, outbound := range []bool{true, false} {
			near, far := uint32(fi), uint32(ti)
			if !outbound {
				near, far = far, near
			}
			ni, fa := slices.Index(p.GetStations(), near), slices.Index(p.GetStations(), far)
			if ni < 0 || fa < 0 || ni > fa {
				continue // doesn't run between them this way
			}
			ai, bi := slices.Index(rem, near), slices.Index(rem, far)
			if ai >= 0 && bi > ai {
				leaves := stops[ai].leave
				if leaves.IsZero() {
					leaves = stops[ai].arrive
				}
				if leaves.After(now.Add(-leftGrace)) {
					d := &pb.Departure{Trip: lt.id, Color: p.GetColor(), Destination: destination(net, p),
						LeavesUnixMs: leaves.UnixMilli(), ArrivesUnixMs: stops[bi].at().UnixMilli()}
					if outbound {
						st.Outbound = append(st.Outbound, d)
					} else {
						st.Inbound = append(st.Inbound, d)
					}
				}
			}
			// Between the two: past the near station (or standing at it, the
			// next stop it makes), not yet at the far one.
			standingAtNear := ai == 0 && !stops[0].at().After(now)
			if bi < 0 || (ai >= 0 && !standingAtNear) {
				continue
			}
			if t := trainAt(net, p, rem[0], stops[0], onLine, now); t != nil {
				t.Trip, t.Outbound, t.Destination = lt.id, outbound, destination(net, p)
				st.Trains = append(st.Trains, t)
			}
		}
	}
	for _, ds := range []*[]*pb.Departure{&st.Outbound, &st.Inbound} {
		sort.SliceStable(*ds, func(i, j int) bool { return (*ds)[i].GetLeavesUnixMs() < (*ds)[j].GetLeavesUnixMs() })
		if len(*ds) > keepDepartures {
			*ds = (*ds)[:keepDepartures]
		}
	}
	sort.SliceStable(st.Trains, func(i, j int) bool { return st.Trains[i].GetTrip() < st.Trains[j].GetTrip() })
	return st
}

// trainAt places a train whose next stop is next on the pair's line: between
// the station before next on its pattern and next, or at next if it's there.
func trainAt(net *pb.Network, p *pb.Pattern, next uint32, s liveStop, onLine map[string]uint32, now time.Time) *pb.Train {
	k := slices.Index(p.GetStations(), next)
	to, ok := onLine[net.GetStations()[next].GetId()]
	if k < 0 || !ok {
		return nil
	}
	t := &pb.Train{Color: p.GetColor(), FromIndex: to, ToIndex: to, ArriveUnixMs: s.at().UnixMilli()}
	if k > 0 && s.at().After(now) {
		if from, ok := onLine[net.GetStations()[p.GetStations()[k-1]].GetId()]; ok {
			t.FromIndex, t.StretchSeconds = from, p.GetSeconds()[k-1]
		}
	}
	return t
}

// lineBetween is the stations from fi to ti on a pattern that runs directly
// between them (either way round, as stations), or nil if none does.
func lineBetween(net *pb.Network, fi, ti uint32) []*pb.Station {
	for _, p := range net.GetPatterns() {
		ss := p.GetStations()
		i, j := slices.Index(ss, fi), slices.Index(ss, ti)
		if i < 0 || j < 0 {
			continue
		}
		var line []*pb.Station
		for k := i; ; {
			line = append(line, net.GetStations()[ss[k]])
			if k == j {
				return line
			}
			if i < j {
				k++
			} else {
				k--
			}
		}
	}
	return nil
}

// reachable is every station a train runs to directly from station fi.
func reachable(net *pb.Network, fi uint32) map[uint32]bool {
	out := map[uint32]bool{}
	for _, p := range net.GetPatterns() {
		ss := p.GetStations()
		if i := slices.Index(ss, fi); i >= 0 {
			for _, s := range ss[i+1:] {
				out[s] = true
			}
		}
	}
	return out
}

func stationIndex(net *pb.Network, id string) int {
	return slices.IndexFunc(net.GetStations(), func(s *pb.Station) bool { return s.GetId() == id })
}

// destination is the station a pattern ends at.
func destination(net *pb.Network, p *pb.Pattern) string {
	ss := p.GetStations()
	return net.GetStations()[ss[len(ss)-1]].GetName()
}
