package server

import (
	"archive/zip"
	"bytes"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	pb "github.com/aren55555/widgetd/examples/bart-tracker/gen/barttracker/v1"
)

// A small BART: the Yellow line runs MONT → EMBR → WOAK → LAFY → ANTC (and
// back, as Yellow-S), the Red line MONT → EMBR → WOAK → RICH. So MONT and
// LAFY are joined by Yellow only, and LAFY and RICH by nothing directly.
var gtfsFiles = map[string]string{
	"stops.txt": "\ufeffstop_id,stop_name,location_type,parent_station\n" +
		"MONT,Montgomery Street,1,\nEMBR,Embarcadero,1,\nWOAK,West Oakland,1,\nLAFY,Lafayette,1,\nANTC,Antioch,1,\nRICH,Richmond,1,\n" +
		"M20-1,Montgomery Street,0,MONT\nM20-2,Montgomery Street,0,MONT\nM16-1,Embarcadero,0,EMBR\nM16-2,Embarcadero,0,EMBR\n" +
		"M10-1,West Oakland,0,WOAK\nM10-2,West Oakland,0,WOAK\nC30-1,Lafayette,0,LAFY\nC30-2,Lafayette,0,LAFY\n" +
		"C80-1,Antioch,0,ANTC\nR60-1,Richmond,0,RICH\nMONT_3,Entrance,2,MONT\n",
	"routes.txt": "route_id,route_color\n1,ffff33\n2,FFFF33\n7,FF0000\n",
	"trips.txt":  "route_id,trip_id\n1,y1\n1,y2\n2,ys\n7,r1\n",
	"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
		// Out of order on purpose: sequence decides.
		"y1,10:02:00,10:02:30,M16-2,2\ny1,10:00:00,10:00:30,M20-2,1\ny1,10:10:00,10:10:30,M10-2,3\n" +
		"y1,10:30:00,10:30:30,C30-2,4\ny1,10:50:00,10:50:00,C80-1,5\n" +
		"y2,10:20:00,10:20:30,M20-2,1\ny2,10:22:00,10:22:30,M16-2,2\ny2,10:30:00,10:30:30,M10-2,3\n" +
		"y2,10:50:00,10:50:30,C30-2,4\ny2,25:10:00,25:10:00,C80-1,5\n" +
		"ys,10:00:00,10:00:30,C80-1,1\nys,10:20:00,10:20:30,C30-1,2\ny-s-not-a-trip,10:00:00,10:00:00,XX-1,1\n" +
		"ys,10:40:00,10:40:30,M10-1,3\nys,10:47:00,10:47:30,M16-1,4\nys,10:49:00,10:49:00,M20-1,5\n" +
		"r1,10:05:00,10:05:30,M20-2,1\nr1,10:07:00,10:07:30,M16-2,2\nr1,10:15:00,10:15:30,M10-2,3\nr1,10:30:00,10:30:00,R60-1,4\n",
}

func gtfsZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range gtfsFiles {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testNetwork(t *testing.T) *pb.Network {
	t.Helper()
	net, err := parseSchedule(gtfsZip(t), time.UnixMilli(1))
	if err != nil {
		t.Fatal(err)
	}
	return net
}

func ids(net *pb.Network, ss []uint32) []string {
	var out []string
	for _, s := range ss {
		out = append(out, net.GetStations()[s].GetId())
	}
	return out
}

func TestParseSchedule(t *testing.T) {
	net := testNetwork(t)
	if len(net.GetStations()) != 6 || len(net.GetPlatforms()) != 10 {
		t.Fatalf("stations %d, platforms %d", len(net.GetStations()), len(net.GetPlatforms()))
	}
	// y1 and y2 call at the same stations on the same route: one pattern.
	if net.GetTrips()["y1"] != net.GetTrips()["y2"] {
		t.Error("y1 and y2 should share a pattern")
	}
	if len(net.GetPatterns()) != 3 {
		t.Fatalf("patterns = %d", len(net.GetPatterns()))
	}
	y := net.GetPatterns()[net.GetTrips()["y1"]]
	if got := ids(net, y.GetStations()); !slices.Equal(got, []string{"MONT", "EMBR", "WOAK", "LAFY", "ANTC"}) {
		t.Errorf("y1 stations = %v", got)
	}
	// Arrival at the next minus departure from this one, from y1 (the first).
	if !slices.Equal(y.GetSeconds(), []uint32{90, 450, 1170, 1170}) || y.GetColor() != "FFFF33" {
		t.Errorf("y1 seconds = %v, colour %s", y.GetSeconds(), y.GetColor())
	}
	if _, ok := net.GetTrips()["y-s-not-a-trip"]; ok {
		t.Error("a trip with no known stations got a pattern")
	}
}

func TestGTFSSeconds(t *testing.T) {
	for in, want := range map[string]int{"10:02:30": 36150, "25:10:00": 90600, "x": -1, "1:2": -1} {
		if got := gtfsSeconds(in); got != want {
			t.Errorf("%q = %d, want %d", in, got, want)
		}
	}
}

// feed encodes a GTFS-realtime FeedMessage of trip updates.
type feedTrip struct {
	id       string
	canceled bool
	stops    []feedStop
}
type feedStop struct {
	platform      string
	arrive, leave int64
	skipped       bool
}

func feed(at int64, trips ...feedTrip) []byte {
	msg := func(fields ...[]byte) []byte { return bytes.Join(fields, nil) }
	bytesField := func(num protowire.Number, v []byte) []byte {
		b := protowire.AppendTag(nil, num, protowire.BytesType)
		return protowire.AppendBytes(b, v)
	}
	varint := func(num protowire.Number, v uint64) []byte {
		b := protowire.AppendTag(nil, num, protowire.VarintType)
		return protowire.AppendVarint(b, v)
	}
	event := func(at int64) []byte { return msg(varint(1, 0), varint(eventTime, uint64(at))) }

	out := bytesField(feedHeader, msg(bytesField(1, []byte("2.0")), varint(headerTimestamp, uint64(at))))
	for _, tr := range trips {
		desc := bytesField(tripID, []byte(tr.id))
		if tr.canceled {
			desc = msg(desc, varint(tripRelationship, tripCanceled))
		}
		update := bytesField(tripUpdateTrip, desc)
		for i, s := range tr.stops {
			stu := msg(varint(1, uint64(i)), bytesField(stuStopID, []byte(s.platform)))
			if s.arrive != 0 {
				stu = msg(stu, bytesField(stuArrival, event(s.arrive)))
			}
			if s.leave != 0 {
				stu = msg(stu, bytesField(stuDeparture, event(s.leave)))
			}
			if s.skipped {
				stu = msg(stu, varint(stuRelationship, stopSkipped))
			}
			update = msg(update, bytesField(tripUpdateSTU, stu))
		}
		out = msg(out, bytesField(feedEntity, msg(bytesField(1, []byte("e-"+tr.id)), bytesField(entityTripUpdate, update))))
	}
	return out
}

func TestParseFeed(t *testing.T) {
	b := feed(1000,
		feedTrip{id: "y1", stops: []feedStop{{platform: "M16-2", arrive: 1010, leave: 1040}, {platform: "M10-2", skipped: true, arrive: 1500}, {platform: "C30-2", arrive: 2000}}},
		feedTrip{id: "gone", canceled: true, stops: []feedStop{{platform: "M20-2", arrive: 1100}}},
		feedTrip{id: "empty"},
	)
	at, trips, err := parseFeed(b)
	if err != nil {
		t.Fatal(err)
	}
	if at.Unix() != 1000 || len(trips) != 1 || trips[0].id != "y1" {
		t.Fatalf("at %v, trips %+v", at, trips)
	}
	s := trips[0].stops
	if len(s) != 2 || s[0].platform != "M16-2" || s[0].leave.Unix() != 1040 || s[1].at().Unix() != 2000 {
		t.Fatalf("stops = %+v", s)
	}
	if _, _, err := parseFeed([]byte{0x0a, 0xff}); err == nil {
		t.Error("accepted a truncated feed")
	}
}

func live(t *testing.T, at int64, trips ...feedTrip) []liveTrip {
	t.Helper()
	_, l, err := parseFeed(feed(at, trips...))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestBuildState(t *testing.T) {
	net := testNetwork(t)
	const now = 10_000
	trips := live(t, now,
		// Waiting to leave MONT for LAFY: a departure outbound.
		feedTrip{id: "y2", stops: []feedStop{{platform: "M20-2", arrive: now + 300, leave: now + 330}, {platform: "M16-2", arrive: now + 420}, {platform: "M10-2", arrive: now + 900}, {platform: "C30-2", arrive: now + 2100}}},
		// Past MONT, between EMBR and WOAK, 200 s from WOAK: in transit outbound.
		feedTrip{id: "y1", stops: []feedStop{{platform: "M10-2", arrive: now + 200}, {platform: "C30-2", arrive: now + 1400}}},
		// Standing at LAFY heading for MONT: in transit (at the station) and a
		// departure inbound.
		feedTrip{id: "ys", stops: []feedStop{{platform: "C30-1", arrive: now - 10, leave: now + 20}, {platform: "M10-1", arrive: now + 1220}, {platform: "M20-1", arrive: now + 1760}}},
		// Red doesn't reach LAFY: nothing.
		feedTrip{id: "r1", stops: []feedStop{{platform: "M16-2", arrive: now + 60}, {platform: "R60-1", arrive: now + 1400}}},
		// Not in the schedule: nothing.
		feedTrip{id: "ghost", stops: []feedStop{{platform: "M20-2", arrive: now + 60}}},
	)
	st := buildState(net, trips, "MONT", "LAFY", time.Unix(now, 0))

	var line []string
	for _, s := range st.GetLine() {
		line = append(line, s.GetId())
	}
	if !slices.Equal(line, []string{"MONT", "EMBR", "WOAK", "LAFY"}) || st.GetFrom().GetName() != "Montgomery Street" {
		t.Fatalf("line = %v, from %v", line, st.GetFrom())
	}
	if len(st.GetOutbound()) != 1 || st.GetOutbound()[0].GetTrip() != "y2" ||
		st.GetOutbound()[0].GetLeavesUnixMs() != (now+330)*1000 || st.GetOutbound()[0].GetArrivesUnixMs() != (now+2100)*1000 ||
		st.GetOutbound()[0].GetDestination() != "Antioch" || st.GetOutbound()[0].GetColor() != "FFFF33" {
		t.Errorf("outbound = %v", st.GetOutbound())
	}
	if len(st.GetInbound()) != 1 || st.GetInbound()[0].GetTrip() != "ys" || st.GetInbound()[0].GetDestination() != "Montgomery Street" {
		t.Errorf("inbound = %v", st.GetInbound())
	}
	if len(st.GetTrains()) != 2 {
		t.Fatalf("trains = %v", st.GetTrains())
	}
	moving, standing := st.GetTrains()[0], st.GetTrains()[1] // sorted by trip: y1, ys
	if moving.GetTrip() != "y1" || !moving.GetOutbound() || moving.GetFromIndex() != 1 || moving.GetToIndex() != 2 ||
		moving.GetStretchSeconds() != 450 || moving.GetArriveUnixMs() != (now+200)*1000 {
		t.Errorf("moving = %v", moving)
	}
	if standing.GetTrip() != "ys" || standing.GetOutbound() || standing.GetFromIndex() != 3 || standing.GetToIndex() != 3 {
		t.Errorf("standing = %v", standing)
	}

	// The same feed seen from the other end: the line runs LAFY → MONT and
	// the directions swap.
	back := buildState(net, trips, "LAFY", "MONT", time.Unix(now, 0))
	if back.GetLine()[0].GetId() != "LAFY" || len(back.GetOutbound()) != 1 || back.GetOutbound()[0].GetTrip() != "ys" {
		t.Errorf("reversed: line %v, outbound %v", back.GetLine(), back.GetOutbound())
	}
	for _, tr := range back.GetTrains() {
		if tr.GetTrip() == "y1" && (tr.GetOutbound() || tr.GetFromIndex() != 2 || tr.GetToIndex() != 1) {
			t.Errorf("reversed y1 = %v", tr)
		}
	}

	// A train that left 40 s ago is gone from the departures; one that left
	// 10 s ago is still there.
	gone := live(t, now,
		feedTrip{id: "y1", stops: []feedStop{{platform: "M20-2", arrive: now - 60, leave: now - 40}, {platform: "C30-2", arrive: now + 1800}}},
		feedTrip{id: "y2", stops: []feedStop{{platform: "M20-2", arrive: now - 30, leave: now - 10}, {platform: "C30-2", arrive: now + 1800}}},
	)
	if out := buildState(net, gone, "MONT", "LAFY", time.Unix(now, 0)).GetOutbound(); len(out) != 1 || out[0].GetTrip() != "y2" {
		t.Errorf("after leaving: %v", out)
	}
}

func TestNoDirectTrains(t *testing.T) {
	net := testNetwork(t)
	st := buildState(net, nil, "LAFY", "RICH", time.Unix(0, 0))
	if st.GetFrom().GetId() != "LAFY" || st.GetTo().GetId() != "RICH" || len(st.GetLine()) != 0 {
		t.Errorf("state = %v", st)
	}
	if st := buildState(net, nil, "NOPE", "RICH", time.Unix(0, 0)); st.GetFrom() != nil {
		t.Errorf("unknown station: %v", st)
	}
}

func TestStationChoices(t *testing.T) {
	net := testNetwork(t)
	c := stationChoices(net, "")
	if len(c["from"]) != 6 || c["from"][0].Label != "Antioch" || len(c["to"]) != 6 {
		t.Fatalf("choices = %v", c)
	}
	// From LAFY: Yellow-S runs on to WOAK, EMBR and MONT; Yellow-N to ANTC.
	var to []string
	for _, ch := range stationChoices(net, "LAFY")["to"] {
		to = append(to, ch.Value)
	}
	if !slices.Equal(to, []string{"ANTC", "EMBR", "MONT", "WOAK"}) {
		t.Errorf("to from LAFY = %v", to)
	}
}

func TestSameBoard(t *testing.T) {
	a := &pb.State{AsOfUnixMs: 1, From: &pb.Station{Id: "MONT"}}
	b := &pb.State{AsOfUnixMs: 2, From: &pb.Station{Id: "MONT"}}
	if !sameBoard(a, b) || sameBoard(a, &pb.State{AsOfUnixMs: 1}) || sameBoard(a, nil) {
		t.Error("sameBoard should ignore only as_of")
	}
	if a.GetAsOfUnixMs() != 1 {
		t.Error("sameBoard changed its argument")
	}
}
