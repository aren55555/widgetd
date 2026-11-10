// Command ical-probe reports the shape of an iCalendar feed without its
// contents: which properties and parameters the events carry and how often,
// enumerated values (PARTSTAT, STATUS, TRANSP, ...), time zones, recurrence,
// and how far back the history goes. Titles, names, emails, locations, notes
// and URLs are never printed, only their lengths, so the report is safe to
// paste into a chat or an issue.
//
//	go run ./spikes/ical-probe basic.ics      # a downloaded feed
//	pbpaste | go run ./spikes/ical-probe -    # the feed's URL on stdin (never printed)
package main

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// Values safe to show as-is: enumerations and time zone names.
var enumParams = map[string]bool{
	"PARTSTAT": true, "ROLE": true, "CUTYPE": true, "RSVP": true, "VALUE": true,
	"TZID": true, "X-NUM-GUESTS": true, "RELATED": true, "FBTYPE": true,
}
var enumProps = map[string]bool{
	"STATUS": true, "TRANSP": true, "CLASS": true, "METHOD": true, "CALSCALE": true,
	"VERSION": true, "PRODID": true, "X-WR-TIMEZONE": true, "ACTION": true, "TZID": true,
}

type prop struct {
	name   string
	params map[string][]string
	value  string
}

type stats struct {
	n, total, max int
}

func (s *stats) add(v int) {
	s.n++
	s.total += v
	s.max = max(s.max, v)
}

func (s stats) String() string {
	if s.n == 0 {
		return "-"
	}
	return fmt.Sprintf("avg %d, max %d", s.total/s.n, s.max)
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ical-probe <file.ics | ->   (- reads the feed URL from stdin)")
		os.Exit(2)
	}
	body, err := load(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ical-probe:", err)
		os.Exit(1)
	}
	report(os.Stdout, body)
}

func load(arg string) ([]byte, error) {
	if arg != "-" {
		return os.ReadFile(arg)
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSpace(line))
	if err != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("stdin is not an https URL")
	}
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(u.String())
	if err != nil {
		// The error text would include the URL; say only where it failed.
		return nil, fmt.Errorf("fetch from %s failed", u.Host)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch from %s: %s", u.Host, resp.Status)
	}
	fmt.Fprintf(os.Stderr, "fetched from %s (Content-Type %q, ETag set: %t, Last-Modified set: %t)\n",
		u.Host, resp.Header.Get("Content-Type"), resp.Header.Get("ETag") != "", resp.Header.Get("Last-Modified") != "")
	return io.ReadAll(resp.Body)
}

// unfold joins RFC 5545 folded lines (a CRLF followed by a space or tab).
func unfold(b []byte) []string {
	raw := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var out []string
	for _, l := range raw {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parse splits NAME;P=V;P="V,W":value, honouring quotes.
func parse(line string) prop {
	p := prop{params: map[string][]string{}}
	inQ, colon := false, -1
	for i, r := range line {
		if r == '"' {
			inQ = !inQ
		}
		if r == ':' && !inQ {
			colon = i
			break
		}
	}
	if colon < 0 {
		return prop{name: strings.ToUpper(line)}
	}
	head, value := line[:colon], line[colon+1:]
	parts := splitUnquoted(head, ';')
	p.name = strings.ToUpper(parts[0])
	for _, kv := range parts[1:] {
		k, v, _ := strings.Cut(kv, "=")
		for _, x := range splitUnquoted(v, ',') {
			p.params[strings.ToUpper(k)] = append(p.params[strings.ToUpper(k)], strings.Trim(x, `"`))
		}
	}
	p.value = value
	return p
}

func splitUnquoted(s string, sep rune) []string {
	var out []string
	inQ, start := false, 0
	for i, r := range s {
		if r == '"' {
			inQ = !inQ
		}
		if r == sep && !inQ {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func report(w io.Writer, body []byte) {
	lines := unfold(body)
	fmt.Fprintf(w, "feed: %d bytes (%.2f MiB; host fetch cap is 4 MiB), %d unfolded lines\n\n", len(body), float64(len(body))/(1<<20), len(lines))

	var (
		stack       []string
		comps       = map[string]int{}
		calProps    = map[string][]string{}
		events      [][]prop
		cur         []prop
		inEvent     bool
		tzids       = map[string]int{}
		inTimezone  bool
		currentTZID string
	)
	for _, l := range lines {
		p := parse(l)
		switch p.name {
		case "BEGIN":
			stack = append(stack, strings.ToUpper(p.value))
			comps[strings.ToUpper(p.value)]++
			if strings.EqualFold(p.value, "VEVENT") && len(stack) == 2 {
				inEvent, cur = true, nil
			}
			if strings.EqualFold(p.value, "VTIMEZONE") {
				inTimezone = true
			}
			continue
		case "END":
			if strings.EqualFold(p.value, "VEVENT") && inEvent && len(stack) == 2 {
				events = append(events, cur)
				inEvent = false
			}
			if strings.EqualFold(p.value, "VTIMEZONE") {
				inTimezone = false
				tzids[currentTZID]++
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		switch {
		case inEvent && len(stack) == 2:
			cur = append(cur, p)
		case inTimezone && p.name == "TZID":
			currentTZID = p.value
		case len(stack) == 1:
			calProps[p.name] = append(calProps[p.name], p.value)
		}
	}

	fmt.Fprintln(w, "components:", fmtCounts(comps))
	fmt.Fprintln(w, "VTIMEZONE TZIDs:", fmtCounts(tzids))
	fmt.Fprintln(w, "calendar properties:")
	for _, k := range sortedKeys(calProps) {
		v := calProps[k][0]
		if !enumProps[k] {
			v = redact(v)
		}
		fmt.Fprintf(w, "  %-22s %s\n", k, v)
	}

	// Per-event property presence, occurrence counts and text lengths.
	type agg struct {
		events int
		occ    stats
		length stats
		params map[string]int
		enums  map[string]map[string]int
	}
	props := map[string]*agg{}
	get := func(n string) *agg {
		if props[n] == nil {
			props[n] = &agg{params: map[string]int{}, enums: map[string]map[string]int{}}
		}
		return props[n]
	}
	var (
		starts           = map[string]int{} // DATE, DATE-TIME UTC, TZID=..., floating
		years            = map[int]int{}
		freq             = map[string]int{}
		descMarks        = map[string]int{}
		withRRule        int
		withRecurrenceID int
		inWindow         int
		now              = time.Now()
		richest          []prop
		richestN         int
	)
	conference := []string{"meet.google.com", "zoom.us", "teams.microsoft.com", "webex.com"}
	for _, ev := range events {
		seen := map[string]int{}
		for _, p := range ev {
			seen[p.name]++
			a := get(p.name)
			a.length.add(len(p.value))
			for k, vs := range p.params {
				a.params[k]++
				if enumParams[k] {
					if a.enums[k] == nil {
						a.enums[k] = map[string]int{}
					}
					for _, v := range vs {
						a.enums[k][v]++
					}
				}
			}
			if enumProps[p.name] {
				if a.enums[""] == nil {
					a.enums[""] = map[string]int{}
				}
				a.enums[""][p.value]++
			}
			switch p.name {
			case "DTSTART":
				switch {
				case len(p.value) == 8 || strings.EqualFold(first(p.params["VALUE"]), "DATE"):
					starts["DATE (all-day)"]++
				case p.params["TZID"] != nil:
					starts["DATE-TIME TZID="+first(p.params["TZID"])]++
				case strings.HasSuffix(p.value, "Z"):
					starts["DATE-TIME UTC"]++
				default:
					starts["DATE-TIME floating"]++
				}
				if len(p.value) >= 8 {
					if t, err := time.Parse("20060102", p.value[:8]); err == nil {
						years[t.Year()]++
						if !t.Before(now.AddDate(0, 0, -1)) && t.Before(now.AddDate(0, 0, 15)) {
							inWindow++
						}
					}
				}
			case "RRULE":
				withRRule++
				for _, part := range strings.Split(p.value, ";") {
					k, v, _ := strings.Cut(part, "=")
					switch k {
					case "FREQ":
						freq[v]++
					case "UNTIL", "COUNT":
						freq["(with "+k+")"]++
					}
				}
			case "RECURRENCE-ID":
				withRecurrenceID++
			case "DESCRIPTION":
				lv := strings.ToLower(p.value)
				for _, c := range conference {
					if strings.Contains(lv, c) {
						descMarks["mentions "+c]++
					}
				}
				if strings.Contains(lv, "<a ") || strings.Contains(lv, "<br") || strings.Contains(lv, "<p>") {
					descMarks["contains HTML"]++
				}
			}
		}
		for n, c := range seen {
			a := get(n)
			a.events++
			a.occ.add(c)
		}
		if len(seen) > richestN || (len(seen) == richestN && seen["ATTENDEE"] > 0 && len(richest) < len(ev)) {
			richest, richestN = ev, len(seen)
		}
	}

	fmt.Fprintf(w, "\nVEVENTs: %d (recurring masters: %d, moved/edited occurrences (RECURRENCE-ID): %d, starting in the next 14 days without recurrence: %d)\n",
		len(events), withRRule, withRecurrenceID, inWindow)
	fmt.Fprintln(w, "DTSTART kinds:", fmtCounts(starts))
	fmt.Fprintln(w, "DTSTART by year:", fmtYears(years))
	fmt.Fprintln(w, "RRULE:", fmtCounts(freq))
	fmt.Fprintln(w, "DESCRIPTION:", fmtCounts(descMarks))

	fmt.Fprintf(w, "\n%-24s %8s  %-20s %-20s %s\n", "property", "events", "per event", "value length", "params")
	names := slices.SortedFunc(func(yield func(string) bool) {
		for k := range props {
			if !yield(k) {
				return
			}
		}
	}, func(a, b string) int { return cmp.Or(cmp.Compare(props[b].events, props[a].events), cmp.Compare(a, b)) })
	for _, n := range names {
		a := props[n]
		pct := 100 * a.events / max(1, len(events))
		fmt.Fprintf(w, "%-24s %7d%%  %-20s %-20s %s\n", n, pct, a.occ, a.length, fmtCounts(a.params))
		for _, k := range sortedKeys(a.enums) {
			label := k + " values"
			if k == "" {
				label = "values"
			}
			fmt.Fprintf(w, "    %-20s %s\n", label, fmtCounts(a.enums[k]))
		}
	}

	if richest != nil {
		fmt.Fprintln(w, "\nthe event with the most kinds of property, redacted:")
		for _, p := range richest {
			fmt.Fprintln(w, "  "+redactLine(p))
		}
	}
}

func redactLine(p prop) string {
	var b strings.Builder
	b.WriteString(p.name)
	for _, k := range sortedKeys(p.params) {
		vs := p.params[k]
		if !enumParams[k] {
			vs = []string{redact(strings.Join(vs, ","))}
		}
		fmt.Fprintf(&b, ";%s=%s", k, strings.Join(vs, ","))
	}
	v := p.value
	switch {
	case enumProps[p.name]:
	case p.name == "DTSTART" || p.name == "DTEND" || p.name == "RECURRENCE-ID" || p.name == "EXDATE" ||
		p.name == "DTSTAMP" || p.name == "CREATED" || p.name == "LAST-MODIFIED":
		v = shape(v)
	case p.name == "RRULE":
		v = redactRRule(v)
	default:
		v = redact(v)
	}
	b.WriteString(":" + v)
	return b.String()
}

// shape keeps a date-time's form, not its value: 20261111T090000Z -> yyyymmddThhmmssZ.
func shape(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= '0' && r <= '9' {
			b.WriteByte('9')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func redactRRule(v string) string {
	var parts []string
	for _, part := range strings.Split(v, ";") {
		k, val, _ := strings.Cut(part, "=")
		if k == "UNTIL" {
			val = shape(val)
		}
		parts = append(parts, k+"="+val)
	}
	return strings.Join(parts, ";")
}

func redact(v string) string {
	if v == "" {
		return "‹empty›"
	}
	kind := "text"
	switch lv := strings.ToLower(v); {
	case strings.HasPrefix(lv, "mailto:"):
		kind = "mailto"
	case strings.HasPrefix(lv, "http"):
		kind = "url"
	}
	return fmt.Sprintf("‹%s·%d›", kind, len(v))
}

func first(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	})
}

func fmtCounts(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	keys := sortedKeys(m)
	slices.SortStableFunc(keys, func(a, b string) int { return cmp.Compare(m[b], m[a]) })
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func fmtYears(m map[int]int) string {
	if len(m) == 0 {
		return "-"
	}
	var ys []int
	for y := range m {
		ys = append(ys, y)
	}
	slices.Sort(ys)
	var parts []string
	for _, y := range ys {
		parts = append(parts, fmt.Sprintf("%d ×%d", y, m[y]))
	}
	return strings.Join(parts, ", ")
}
