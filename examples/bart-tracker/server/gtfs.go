package server

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	pb "github.com/aren55555/widgetd/examples/bart-tracker/gen/barttracker/v1"
)

// parseSchedule reads BART's static GTFS zip into a Network: its stations,
// which platform belongs to which, every distinct stopping pattern with its
// line colour and scheduled running times, and which trip runs which.
func parseSchedule(zipped []byte, now time.Time) (*pb.Network, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	net := &pb.Network{FetchedUnixMs: now.UnixMilli(), Platforms: map[string]uint32{}, Trips: map[string]uint32{}}

	// Stations (location_type 1), then the platforms within them.
	station := map[string]uint32{}
	type platform struct{ id, parent string }
	var platforms []platform
	err = eachRow(zr, "stops.txt", func(r row) error {
		switch r.get("location_type") {
		case "1":
			station[r.get("stop_id")] = uint32(len(net.GetStations()))
			net.Stations = append(net.Stations, &pb.Station{Id: r.get("stop_id"), Name: r.get("stop_name")})
		case "", "0":
			if p := r.get("parent_station"); p != "" {
				platforms = append(platforms, platform{r.get("stop_id"), p})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, p := range platforms {
		if i, ok := station[p.parent]; ok {
			net.Platforms[p.id] = i
		}
	}

	colors := map[string]string{}
	if err := eachRow(zr, "routes.txt", func(r row) error {
		colors[r.get("route_id")] = r.get("route_color")
		return nil
	}); err != nil {
		return nil, err
	}
	routeOf := map[string]string{}
	if err := eachRow(zr, "trips.txt", func(r row) error {
		routeOf[r.get("trip_id")] = r.get("route_id")
		return nil
	}); err != nil {
		return nil, err
	}

	type call struct {
		seq        int
		station    uint32
		arr, dep   int
		hasStation bool
	}
	calls := map[string][]call{}
	err = eachRow(zr, "stop_times.txt", func(r row) error {
		seq, err := strconv.Atoi(r.get("stop_sequence"))
		if err != nil {
			return fmt.Errorf("stop_times.txt: stop_sequence %q", r.get("stop_sequence"))
		}
		st, ok := net.Platforms[r.get("stop_id")]
		trip := r.get("trip_id")
		calls[trip] = append(calls[trip], call{seq: seq, station: st, hasStation: ok,
			arr: gtfsSeconds(r.get("arrival_time")), dep: gtfsSeconds(r.get("departure_time"))})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// One pattern per line and sequence of stations; trips sorted so the
	// network (and so what's stored) comes out the same every time.
	trips := make([]string, 0, len(calls))
	for t := range calls {
		trips = append(trips, t)
	}
	sort.Strings(trips)
	patternOf := map[string]uint32{}
	for _, t := range trips {
		cs := calls[t]
		sort.Slice(cs, func(i, j int) bool { return cs[i].seq < cs[j].seq })
		p := &pb.Pattern{Color: strings.ToUpper(colors[routeOf[t]])}
		var key strings.Builder
		key.WriteString(routeOf[t])
		prevDep := -1
		for _, c := range cs {
			if !c.hasStation {
				continue
			}
			if prevDep >= 0 {
				p.Seconds = append(p.Seconds, uint32(max(0, c.arr-prevDep)))
			}
			p.Stations = append(p.Stations, c.station)
			prevDep = c.dep
			fmt.Fprintf(&key, ",%d", c.station)
		}
		if len(p.GetStations()) < 2 {
			continue
		}
		i, ok := patternOf[key.String()]
		if !ok {
			i = uint32(len(net.GetPatterns()))
			patternOf[key.String()] = i
			net.Patterns = append(net.Patterns, p)
		}
		net.Trips[t] = i
	}
	if len(net.GetStations()) == 0 || len(net.GetPatterns()) == 0 {
		return nil, errors.New("schedule: no stations or no trips")
	}
	return net, nil
}

// gtfsSeconds is a GTFS time, "25:07:00" (past midnight of a long service
// day included), in seconds; -1 if it isn't one.
func gtfsSeconds(s string) int {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return -1
	}
	n := 0
	for _, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return -1
		}
		n = n*60 + v
	}
	return n
}

// row is one CSV record, by header name.
type row struct {
	cols   map[string]int
	record []string
}

func (r row) get(name string) string {
	if i, ok := r.cols[name]; ok && i < len(r.record) {
		return r.record[i]
	}
	return ""
}

// eachRow calls f for every record of one of the zip's CSV files.
func eachRow(zr *zip.Reader, name string, f func(row) error) error {
	file, err := zr.Open(name)
	if err != nil {
		return fmt.Errorf("schedule: %w", err)
	}
	defer file.Close()
	cr := csv.NewReader(file)
	cr.ReuseRecord = true
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("schedule: %s: %w", name, err)
	}
	cols := map[string]int{}
	for i, h := range header {
		cols[strings.TrimPrefix(strings.TrimSpace(h), "\ufeff")] = i
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("schedule: %s: %w", name, err)
		}
		if err := f(row{cols, rec}); err != nil {
			return err
		}
	}
}
