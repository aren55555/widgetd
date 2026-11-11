package server

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/calendar/gen/calendar/v1"
)

// now is a Tuesday; the window starts eight UTC days before its midnight.
var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []gEvent {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Items []gEvent `json:"items"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Items
}

func both(t *testing.T) [][]gEvent {
	t.Helper()
	return [][]gEvent{fixture(t, "work.json"), fixture(t, "personal.json")}
}

func valid(t *testing.T, st *pb.State) {
	t.Helper()
	if err := protovalidate.Validate(st); err != nil {
		t.Fatalf("state breaks its rules: %v", err)
	}
}

// at is a time's place on the timeline, in units from the window's start.
func at(s string) uint32 {
	from, _ := window(now)
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return uint32(ts.Sub(from) / unit)
}

func TestWindow(t *testing.T) {
	from, to := window(now)
	if want := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC); !from.Equal(want) {
		t.Fatalf("from %v, want %v", from, want)
	}
	if to.Sub(from) != 24*day {
		t.Fatalf("window is %v", to.Sub(from))
	}
	// It only moves at UTC midnight.
	if f, _ := window(now.Add(11*time.Hour + 59*time.Minute)); !f.Equal(from) {
		t.Fatal("window moved within the day")
	}
	// Every zone's "this Sunday" through the fortnight after it fits.
	for _, zone := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago", "America/Los_Angeles", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skip("no zone database")
		}
		for h := range 7 * 24 {
			n := now.Add(time.Duration(h) * time.Hour)
			from, to := window(n)
			local := n.In(loc)
			sunday := time.Date(local.Year(), local.Month(), local.Day()-int(local.Weekday()), 0, 0, 0, 0, loc)
			end := sunday.AddDate(0, 0, 14)
			if sunday.Before(from) || end.After(to) {
				t.Fatalf("%s at %v: needs %v–%v, window %v–%v", zone, n, sunday, end, from, to)
			}
		}
	}
}

func TestBuildTimeline(t *testing.T) {
	st := build(now, resolve(&pb.Config{}), both(t))
	valid(t, st)

	early := []uint32{0, at("2026-11-02T01:00:00Z")}
	standup := []uint32{at("2026-11-10T17:00:00Z"), 6}
	overlap := []uint32{at("2026-11-10T17:15:00Z"), 9}
	offgrid := []uint32{at("2026-11-12T10:50:00Z"), 7} // 10:52–11:21 → 10:50–11:25
	wantBusy := []uint32{
		early[0], early[1],
		standup[0] - early[0], standup[1],
		overlap[0] - standup[0], overlap[1],
		offgrid[0] - overlap[0], offgrid[1],
	}
	if !slices.Equal(st.Busy, wantBusy) {
		t.Errorf("busy %v, want %v", st.Busy, wantBusy)
	}
	review, climb := at("2026-11-11T18:00:00Z"), at("2026-11-14T02:00:00Z")
	if want := []uint32{review, 12, climb - review, 24}; !slices.Equal(st.Tentative, want) {
		t.Errorf("tentative %v, want %v (unanswered, and marked free)", st.Tentative, want)
	}

	var titles []string
	for _, e := range st.Events {
		titles = append(titles, e.Title)
	}
	want := []string{"Started before the window", "Standup", "Customer call", "Off the grid", "Design review", "Climbing"}
	if !slices.Equal(titles, want) {
		t.Errorf("events %q, want %q (declined, cancelled, working location and the shared copy left out)", titles, want)
	}

	offsite := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC).Unix() / 86400
	if len(st.AllDay) != 1 || int64(st.AllDay[0].FirstDay) != offsite || st.AllDay[0].Days != 2 || st.AllDay[0].Title != "Team offsite" {
		t.Errorf("all day %v", st.AllDay)
	}
}

func TestBuildFullEvent(t *testing.T) {
	st := build(now, resolve(&pb.Config{}), both(t))
	e := st.Events[1]
	want := &pb.Event{
		Title:     "Standup",
		Where:     "Juniper (4B)", // the room, as the event names no place
		Video:     "meet.google.com/abc-defg-hij",
		Organizer: "Priya Shah",
		Notes:     "Yesterday / today / blockers.\nKeep it < 15 minutes.",
		You:       pb.Response_RESPONSE_ACCEPTED,
		Guests: []*pb.Guest{
			{Name: "You", Response: pb.Response_RESPONSE_ACCEPTED, Self: true},
			{Name: "Priya Shah", Response: pb.Response_RESPONSE_ACCEPTED, Organizer: true},
			{Name: "marcus", Response: pb.Response_RESPONSE_TENTATIVE},
		},
		GuestCount: 3,
	}
	if !proto.Equal(e, want) {
		t.Errorf("got  %v\nwant %v", e, want)
	}
	if climb := st.Events[5]; climb.Organizer != "You" {
		t.Errorf("your own event's organizer is %q", climb.Organizer)
	}
}

func TestBuildDetail(t *testing.T) {
	titles := build(now, resolve(&pb.Config{Detail: pb.Detail_DETAIL_TITLES}), both(t))
	valid(t, titles)
	for _, e := range titles.Events {
		if e.Where != "" || e.Notes != "" || len(e.Guests) > 0 || e.Organizer != "" {
			t.Errorf("titles level carries %v", e)
		}
	}
	busy := build(now, resolve(&pb.Config{Detail: pb.Detail_DETAIL_BUSY}), both(t))
	valid(t, busy)
	if len(busy.Events) != 0 || busy.AllDay[0].Title != "" {
		t.Errorf("busy level carries titles: %v %v", busy.Events, busy.AllDay)
	}
	if !slices.Equal(busy.Busy, titles.Busy) || !slices.Equal(busy.Tentative, titles.Tentative) {
		t.Error("the timeline depends on the detail level")
	}
}

func TestBuildStable(t *testing.T) {
	// The same events give the same state, so an unchanged calendar sends
	// nothing; an hour later, too, as the window only moves at midnight.
	a := build(now, resolve(&pb.Config{}), both(t))
	b := build(now.Add(time.Hour), resolve(&pb.Config{}), both(t))
	if !proto.Equal(a, b) {
		t.Fatal("same calendar, different state")
	}
}

func TestBuildMany(t *testing.T) {
	// A packed fortnight of back-to-back meetings stays well inside the
	// card's limits.
	var evs []gEvent
	from, _ := window(now)
	for d := range 24 {
		for h := 8; h < 18; h++ {
			s := from.Add(time.Duration(d)*day + time.Duration(h)*time.Hour)
			evs = append(evs, gEvent{ICalUID: s.String(), Summary: strings.Repeat("x", 300),
				Start: gTime{DateTime: s.Format(time.RFC3339)}, End: gTime{DateTime: s.Add(time.Hour).Format(time.RFC3339)}})
		}
	}
	st := build(now, resolve(&pb.Config{}), [][]gEvent{evs})
	valid(t, st)
	b, _ := proto.Marshal(st)
	if len(b) > 64<<10 {
		t.Fatalf("%d bytes", len(b))
	}
}

func TestResolve(t *testing.T) {
	s := resolve(&pb.Config{})
	if !slices.Equal(s.calendars, []string{"primary"}) || s.detail != pb.Detail_DETAIL_FULL {
		t.Errorf("defaults: %v %v", s.calendars, s.detail)
	}
	if w := s.work; w.Start != 9*60 || w.End != 17*60+30 || w.Days != 0b0111110 {
		t.Errorf("default hours %v", w)
	}
	s = resolve(&pb.Config{WorkStart: "08:15", WorkEnd: "24:00", WorkDays: []pb.Weekday{pb.Weekday_WEEKDAY_SUNDAY, pb.Weekday_WEEKDAY_SATURDAY}})
	if w := s.work; w.Start != 8*60+15 || w.End != 24*60 || w.Days != 0b1000001 {
		t.Errorf("set hours %v", w)
	}
}

func TestPlainAndClip(t *testing.T) {
	if got := plain("Agenda:<br>1. <b>Q1</b> &amp; Q2<br/><br/>\n\n  <a href=\"x\">doc</a>"); got != "Agenda:\n1. Q1 & Q2\ndoc" {
		t.Errorf("plain: %q", got)
	}
	if got := clip("héllo wörld", 7); got != "héllo…" {
		t.Errorf("clip: %q", got)
	}
	if got := clip("short", 7); got != "short" {
		t.Errorf("clip: %q", got)
	}
}
