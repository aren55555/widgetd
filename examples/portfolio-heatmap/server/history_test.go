package server

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

// mapKV is a widget.KV in memory.
type mapKV map[string][]byte

func (m mapKV) Get(k string) ([]byte, bool, error) { b, ok := m[k]; return b, ok, nil }
func (m mapKV) Put(k string, v []byte) error       { m[k] = v; return nil }
func (m mapKV) Delete(k string) error              { delete(m, k); return nil }

func day(s string) time.Time {
	t, err := time.Parse(dayLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestTradingDay(t *testing.T) {
	et := func(s string, offset int) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v.Add(-time.Duration(offset) * time.Hour) // Eastern → UTC
	}
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{et("2026-11-13 04:00", -5), "2026-11-13"}, // pre-market, winter
		{et("2026-11-13 16:00", -5), "2026-11-13"}, // the close
		{et("2026-11-13 20:00", -5), "2026-11-13"}, // the end of after-hours
		{et("2026-07-10 04:00", -4), "2026-07-10"}, // summer
		{et("2026-07-10 20:00", -4), "2026-07-10"},
	} {
		if got := tradingDay(tc.at).Format(dayLayout); got != tc.want {
			t.Errorf("tradingDay(%v) = %s, want %s", tc.at, got, tc.want)
		}
	}
	for d, want := range map[string]string{
		"2026-11-16": "2026-11-13", // Monday → Friday
		"2026-11-14": "2026-11-13", // Saturday → Friday
		"2026-11-13": "2026-11-12",
	} {
		if got := dayBefore(day(d)).Format(dayLayout); got != want {
			t.Errorf("dayBefore(%s) = %s, want %s", d, got, want)
		}
	}
}

func TestRecordClose(t *testing.T) {
	kv := mapKV{}
	hs := newHistory(kv)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(hs.recordClose("AAPL", "2026-11-11", 100))
	must(hs.recordClose("AAPL", "2026-11-12", 101))
	must(hs.recordClose("AAPL", "2026-11-12", 101)) // every quote that day: no change
	must(hs.recordClose("AAPL", "2026-11-10", 99))  // out of order: put in its place
	// The day after a holiday on the 13th: the previous close is the 12th's
	// again, under the holiday's date. It is dropped.
	must(hs.recordClose("AAPL", "2026-11-13", 101))
	must(hs.recordClose("AAPL", "", 5)) // a quote without a time
	must(hs.recordClose("AAPL", "2026-11-16", 0))

	want := []point{{"2026-11-10", 99}, {"2026-11-11", 100}, {"2026-11-12", 101}}
	var stored []point
	if err := json.Unmarshal(kv["closes/AAPL"], &stored); err != nil || !slices.Equal(stored, want) {
		t.Fatalf("stored %v (%v), want %v", stored, err, want)
	}

	// A restart reads them back.
	again := newHistory(kv)
	if got, err := again.series("AAPL"); err != nil || !slices.Equal(got, want) {
		t.Fatalf("after a restart: %v (%v)", got, err)
	}

	// Only the newest are kept.
	for i := range keepCloses + 5 {
		must(again.recordClose("MSFT", day("2026-01-01").AddDate(0, 0, i).Format(dayLayout), float64(100+i)))
	}
	got, _ := again.series("MSFT")
	if len(got) != keepCloses || got[0].Price != 105 {
		t.Errorf("kept %d, first %v", len(got), got[0])
	}
}

func TestMonthPct(t *testing.T) {
	today := day("2026-11-13")
	pts := []point{
		{"2026-10-01", 50}, // over a month ago: left out
		{"2026-11-10", 100},
		{"2026-11-11", 110},
		{"2026-11-12", 90},
	}
	m, from := monthPct(pts, 105, "2026-11-13", today)
	if !slices.Equal(m, []float32{0, 10, -10, 5}) || !from.Equal(day("2026-11-10")) {
		t.Errorf("got %v from %v", m, from)
	}
	// The live price replaces its own day's close (a coin's moving candle).
	if m, _ := monthPct(pts, 99, "2026-11-12", today); !slices.Equal(m, []float32{0, 10, -1}) {
		t.Errorf("live on the last close's day: %v", m)
	}
	// No quote: the closes alone.
	if m, _ := monthPct(pts, 0, "", today); len(m) != 3 {
		t.Errorf("no live price: %v", m)
	}
	// Under two points there's no chart: a stock's first day.
	if m, _ := monthPct(nil, 105, "2026-11-13", today); m != nil {
		t.Errorf("one point: %v", m)
	}
	if m, _ := monthPct([]point{{"2026-11-12", 100}}, 101, "2026-11-13", today); len(m) != 2 {
		t.Errorf("a close and a quote: %v", m)
	}
}

func TestBuildMonth(t *testing.T) {
	ps := []position{
		{Symbol: "AAPL", Units: 1, Price: 100},
		{Symbol: "NEW", Units: 1, Price: 100},
	}
	quotes := map[string]quote{"AAPL": {Price: 120, Day: "2026-11-13"}, "NEW": {Price: 100, Day: "2026-11-13"}}
	closes := map[string][]point{}
	for i := range 25 {
		closes["AAPL"] = append(closes["AAPL"], point{day("2026-10-15").AddDate(0, 0, i).Format(dayLayout), 100 + float64(i)})
	}
	st := build(asOf, ps, quotes, closes, day("2026-11-13"))
	valid(t, st)
	h := byName(st)
	a := h["AAPL"]
	if n := len(a.GetMonthPct()); n != 26 || a.GetMonthPct()[n-1] != 20 || a.GetMonthFromUnixMs() != day("2026-10-15").UnixMilli() {
		t.Errorf("AAPL month = %v from %d", a.GetMonthPct(), a.GetMonthFromUnixMs())
	}
	if n := h["NEW"]; len(n.GetMonthPct()) != 0 || n.GetMonthFromUnixMs() != 0 {
		t.Errorf("a stock with no closes yet has a chart: %v", fmt.Sprint(n.GetMonthPct()))
	}
}

func TestMonthDue(t *testing.T) {
	hs := newHistory(nil)
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	if !hs.monthDue("GOOG", now) {
		t.Fatal("a month never read is due")
	}
	hs.monthFailed("GOOG", now)
	if hs.monthDue("GOOG", now.Add(monthRetry/2)) || !hs.monthDue("GOOG", now.Add(monthRetry)) {
		t.Error("a failed read should wait monthRetry")
	}
	hs.closes["GOOG"] = []point{{"2026-11-13", 100}}
	hs.setMonth("GOOG", now, nil)
	if len(hs.closes["GOOG"]) != 1 {
		t.Error("an empty month should keep the closes there are")
	}
	hs.setMonth("GOOG", now, []point{{"2026-10-20", 90}, {"2026-11-13", 100}})
	if len(hs.closes["GOOG"]) != 2 || hs.monthDue("GOOG", now.Add(2*time.Hour)) || !hs.monthDue("GOOG", now.Add(10*time.Hour)) {
		t.Error("a month read today is due again tomorrow")
	}
}
