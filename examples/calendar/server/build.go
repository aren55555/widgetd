package server

import (
	"html"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/aren55555/widgetd/examples/calendar/gen/calendar/v1"
)

const (
	unit       = 5 * time.Minute
	day        = 24 * time.Hour
	windowBack = 8 * day  // this week's Sunday, in any zone, is within this
	windowLen  = 24 * day // ... and the fortnight after it, ±14 h
	maxGuests  = 30
)

// window is the stretch of time the card covers: whole UTC days, so it
// moves once a day, wide enough that every screen can cut "this Sunday plus
// 14 days" out of it in its own time zone.
func window(now time.Time) (from, to time.Time) {
	from = now.UTC().Truncate(day).Add(-windowBack)
	return from, from.Add(windowLen)
}

// settings is Config with its defaults filled in.
type settings struct {
	calendars []string
	work      *pb.WorkingHours
	detail    pb.Detail
}

func resolve(c *pb.Config) settings {
	s := settings{calendars: c.GetCalendars(), detail: c.GetDetail()}
	if len(s.calendars) == 0 {
		s.calendars = []string{"primary"}
	}
	if s.detail == pb.Detail_DETAIL_UNSPECIFIED {
		s.detail = pb.Detail_DETAIL_FULL
	}
	s.work = &pb.WorkingHours{Start: clock(c.GetWorkStart(), 9*60), End: clock(c.GetWorkEnd(), 17*60+30)}
	for _, d := range c.GetWorkDays() {
		s.work.Days |= 1 << (uint32(d) - 1)
	}
	if s.work.Days == 0 {
		s.work.Days = 0b0111110 // Monday to Friday
	}
	return s
}

// clock reads "HH:MM" as minutes after midnight; def if empty or malformed.
func clock(s string, def uint32) uint32 {
	t, err := time.Parse("15:04", s)
	if s == "24:00" {
		return 24 * 60
	}
	if err != nil {
		return def
	}
	return uint32(t.Hour()*60 + t.Minute())
}

// span is a timed event placed on the timeline, in units from its start.
type span struct {
	start, end uint32
	tentative  bool
	ev         *pb.Event
}

// build turns the calendars' events into the card's state. Calendars are
// merged; an event that's on more than one (an invitation copied to a
// shared calendar) is shown once.
func build(now time.Time, s settings, cals [][]gEvent) *pb.State {
	from, to := window(now)
	total := uint32(to.Sub(from) / unit)
	st := &pb.State{
		WindowStart:       from.Unix(),
		ResolutionMinutes: uint32(unit / time.Minute),
		WorkingHours:      s.work,
		Detail:            s.detail,
	}
	seen := map[string]bool{}
	var spans []span
	for _, evs := range cals {
		for i := range evs {
			e := &evs[i]
			if e.Status == "cancelled" || e.EventType == "workingLocation" {
				continue
			}
			you := response(e)
			if you == pb.Response_RESPONSE_DECLINED {
				continue
			}
			key := e.ICalUID + "\x00" + e.Start.DateTime + e.Start.Date
			if e.ICalUID != "" && seen[key] {
				continue
			}
			seen[key] = true

			if e.Start.Date != "" {
				if ad, ok := allDay(e, from, to); ok {
					if s.detail != pb.Detail_DETAIL_BUSY {
						ad.Title = clip(e.Summary, 200)
					}
					st.AllDay = append(st.AllDay, ad)
				}
				continue
			}
			a, err1 := time.Parse(time.RFC3339, e.Start.DateTime)
			b, err2 := time.Parse(time.RFC3339, e.End.DateTime)
			if err1 != nil || err2 != nil {
				continue
			}
			// Outside the window (an event ending exactly at its start
			// included, unless it has no length at all).
			if !a.Before(to) || b.Before(from) || (b.Equal(from) && a.Before(b)) {
				continue
			}
			// Floor the start and ceil the end, so a 10:50–11:20 event
			// doesn't shrink, and clip to the window. A zero-length event
			// still shows, as one unit.
			lo := max(0, a.Sub(from)) / unit
			hi := (min(b.Sub(from), to.Sub(from)) + unit - 1) / unit
			if hi <= lo {
				hi = lo + 1
			}
			sp := span{
				start:     uint32(lo),
				end:       min(uint32(hi), total),
				tentative: e.Transparency == "transparent" || you == pb.Response_RESPONSE_TENTATIVE || you == pb.Response_RESPONSE_NEEDS_ACTION,
			}
			if s.detail != pb.Detail_DETAIL_BUSY {
				sp.ev = event(e, you, s.detail)
			}
			spans = append(spans, sp)
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end < spans[j].end
	})
	sort.SliceStable(st.AllDay, func(i, j int) bool { return st.AllDay[i].FirstDay < st.AllDay[j].FirstDay })

	var busy, tent []span
	for _, sp := range spans {
		if sp.tentative {
			tent = append(tent, sp)
		} else {
			busy = append(busy, sp)
		}
	}
	st.Busy = pack(busy)
	st.Tentative = pack(tent)
	if s.detail != pb.Detail_DETAIL_BUSY {
		for _, sp := range append(busy, tent...) {
			st.Events = append(st.Events, sp.ev)
		}
	}
	return st
}

// pack writes spans (in start order) as [start − previous start, duration]
// pairs: small numbers, so a fortnight packs into a few hundred bytes.
func pack(spans []span) []uint32 {
	out := make([]uint32, 0, 2*len(spans))
	prev := uint32(0)
	for _, sp := range spans {
		out = append(out, sp.start-prev, sp.end-sp.start)
		prev = sp.start
	}
	return out
}

// allDay places an all-day event, clipped to the window's days.
func allDay(e *gEvent, from, to time.Time) (*pb.AllDay, bool) {
	a, err1 := time.Parse(time.DateOnly, e.Start.Date)
	b, err2 := time.Parse(time.DateOnly, e.End.Date)
	if err1 != nil || err2 != nil {
		return nil, false
	}
	if !b.After(a) {
		b = a.Add(day)
	}
	a, b = maxTime(a, from), minTime(b, to)
	if !b.After(a) {
		return nil, false
	}
	return &pb.AllDay{FirstDay: int32(a.Unix() / 86400), Days: uint32(b.Sub(a) / day)}, true
}

// response is what the calendar's owner said to the event. Their own
// events, with no guest list, count as accepted.
func response(e *gEvent) pb.Response {
	for _, a := range e.Attendees {
		if a.Self {
			return responseOf(a.ResponseStatus)
		}
	}
	if e.Status == "tentative" {
		return pb.Response_RESPONSE_TENTATIVE
	}
	return pb.Response_RESPONSE_UNSPECIFIED
}

func responseOf(s string) pb.Response {
	switch s {
	case "accepted":
		return pb.Response_RESPONSE_ACCEPTED
	case "tentative":
		return pb.Response_RESPONSE_TENTATIVE
	case "declined":
		return pb.Response_RESPONSE_DECLINED
	case "needsAction":
		return pb.Response_RESPONSE_NEEDS_ACTION
	}
	return pb.Response_RESPONSE_UNSPECIFIED
}

// event is what the card says about one event, as far as detail allows.
func event(e *gEvent, you pb.Response, detail pb.Detail) *pb.Event {
	ev := &pb.Event{Title: clip(e.Summary, 200)}
	if ev.Title == "" {
		ev.Title = "(No title)"
	}
	if detail != pb.Detail_DETAIL_FULL {
		return ev
	}
	ev.You = you
	ev.Where = clip(e.Location, 200)
	ev.Video = clip(video(e), 200)
	ev.Notes = clip(plain(e.Description), 300)
	switch {
	case e.Organizer.Self:
		ev.Organizer = "You"
	default:
		ev.Organizer = clip(nameOf(e.Organizer.DisplayName, e.Organizer.Email), 120)
	}
	var guests []*pb.Guest
	for _, a := range e.Attendees {
		if a.Resource {
			// A room: where it is, if the event doesn't say otherwise.
			if ev.Where == "" {
				ev.Where = clip(nameOf(a.DisplayName, a.Email), 200)
			}
			continue
		}
		name := nameOf(a.DisplayName, a.Email)
		if a.Self {
			name = "You"
		}
		guests = append(guests, &pb.Guest{Name: clip(name, 120), Response: responseOf(a.ResponseStatus), Organizer: a.Organizer, Self: a.Self})
	}
	// You first, then the organizer, then everyone else as Google lists them.
	sort.SliceStable(guests, func(i, j int) bool { return rank(guests[i]) < rank(guests[j]) })
	ev.GuestCount = uint32(len(guests))
	ev.Guests = guests[:min(len(guests), maxGuests)]
	return ev
}

func rank(g *pb.Guest) int {
	switch {
	case g.Self:
		return 0
	case g.Organizer:
		return 1
	}
	return 2
}

// video is the event's video call link, without its scheme.
func video(e *gEvent) string {
	u := e.HangoutLink
	for _, p := range e.ConferenceData.EntryPoints {
		if u == "" && p.EntryPointType == "video" {
			u = p.URI
		}
	}
	return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
}

// nameOf is a person's name, else the part of their address before the @.
func nameOf(name, email string) string {
	if name != "" {
		return name
	}
	if at := strings.IndexByte(email, '@'); at > 0 {
		return email[:at]
	}
	return email
}

var (
	breaks = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>`)
	tags   = regexp.MustCompile(`<[^>]*>`)
	spaces = regexp.MustCompile(`[ \t\r\f\v]+`)
	blanks = regexp.MustCompile(`\n\s*\n+`)
)

// plain is a description as plain text: Google stores them as HTML.
func plain(s string) string {
	s = breaks.ReplaceAllString(s, "\n")
	s = tags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = spaces.ReplaceAllString(s, " ")
	s = blanks.ReplaceAllString(s, "\n")
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(lines, "\n")
}

// clip shortens s to at most n characters, ending in "…" if it was cut.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
