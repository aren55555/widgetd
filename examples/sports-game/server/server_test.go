package server

import (
	"testing"
	"time"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

func TestWindow(t *testing.T) {
	const (
		h       = time.Hour
		start   = 100 * h
		before  = 3 * h
		after   = 24 * h
		typical = 3 * h
	)
	cases := []struct {
		name      string
		end       time.Duration
		completed bool
		now       time.Duration
		show      bool
		until     time.Duration
	}{
		{"before the window", 0, false, start - 4*h, false, 0},
		{"opens three hours before", 0, false, start - 3*h, true, start + 2*typical + after},
		{"live, end unknown: generous", 0, false, start + h, true, start + 2*typical + after},
		{"seen ending: a day after the end", start + 2*h + 30*time.Minute, true, start + 3*h, true, start + 2*h + 30*time.Minute + after},
		{"gone a day after it ended", start + 2*h, true, start + 2*h + after, false, start + 2*h + after},
		{"completed while we weren't looking: usual length", 0, true, start + 5*h, true, start + typical + after},
		{"postponed, never completed: eventually gone", 0, false, start + 2*typical + after + h, false, start + 2*typical + after},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			show, until := window(start, tc.end, tc.completed, before, after, typical, tc.now)
			if show != tc.show || (tc.until != 0 && until != tc.until) {
				t.Fatalf("show=%v until=%v, want %v %v", show, until, tc.show, tc.until)
			}
		})
	}
}

func TestParseESPNTime(t *testing.T) {
	got, err := parseESPNTime("2026-11-08T18:00Z")
	if err != nil || !got.Equal(time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parseESPNTime("tomorrow"); err == nil {
		t.Fatal("junk should not parse")
	}
}

func TestSuggest(t *testing.T) {
	teams := []widget.Choice{{Value: "SJ", Label: "San Jose Sharks"}, {Value: "SEA", Label: "Seattle Kraken"}, {Value: "STL", Label: "St. Louis Blues"}}
	if got := suggest("SJS", teams); len(got) != 1 || got[0] != "SJ (San Jose Sharks)" {
		t.Fatalf("SJS → %v", got)
	}
	if got := suggest("ZZZ", teams); len(got) != 0 {
		t.Fatalf("ZZZ → %v", got)
	}
}
