package server

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"

	pb "github.com/aren55555/widgetd/examples/portfolio-heatmap/gen/portfolio/v1"
)

var asOf = time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC)

func valid(t *testing.T, st *pb.State) {
	t.Helper()
	if err := protovalidate.Validate(st); err != nil {
		t.Fatalf("state breaks its rules: %v", err)
	}
}

func byName(st *pb.State) map[string]*pb.Holding {
	m := map[string]*pb.Holding{}
	for _, h := range st.GetHoldings() {
		m[h.GetSymbol()] = h
	}
	return m
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.006 }

func TestBuild(t *testing.T) {
	ps := []position{
		{Symbol: "AAPL", Name: "Apple", Units: 10, Price: 200, Cost: 150},                       // 2000
		{Symbol: "VOO", Name: "Vanguard", Units: 2.5, Price: 500},                               // 1250, no cost
		{Symbol: "BTC", Name: "Bitcoin", Crypto: true, Units: 0.01, Price: 100000, Cost: 50000}, // 1000
		{Symbol: "AAPL", Units: 5, Price: 200, Cost: 180},                                       // AAPL in a second account: 1000
		{Symbol: "TSLA", Units: -3, Price: 250, Cost: 260},                                      // short: left out
		{Symbol: "DUD", Units: 4},                                                               // no price: left out
	}
	quotes := map[string]quote{
		"VOO": {Price: 500, DayPct: -0.333},
		"BTC": {Price: 110000, DayPct: 2.5}, // 1100
	}
	st := build(asOf, ps, quotes, nil, asOf)
	valid(t, st)
	if st.GetAsOfUnixMs() != asOf.UnixMilli() {
		t.Errorf("as of %d", st.GetAsOfUnixMs())
	}
	var syms []string
	var sum int32
	for _, h := range st.GetHoldings() {
		syms = append(syms, h.GetSymbol())
		sum += h.GetWeightBp()
	}
	if got := strings.Join(syms, " "); got != "AAPL VOO BTC" {
		t.Fatalf("order %q, want largest first", got)
	}
	if sum < 9998 || sum > 10002 {
		t.Errorf("weights sum to %d bp", sum)
	}
	m := byName(st)
	// 3000 of 5350
	if w := m["AAPL"].GetWeightBp(); w != 5607 {
		t.Errorf("AAPL weight %d bp", w)
	}
	// cost 1500 + 900 = 2400 for 3000 now
	if r := m["AAPL"].GetTotalPct(); !near(r, 25) {
		t.Errorf("AAPL total %v", r)
	}
	if m["AAPL"].DayPct != nil {
		t.Errorf("AAPL has a day change without a quote")
	}
	if m["VOO"].TotalPct != nil {
		t.Errorf("VOO has a total return without a cost basis")
	}
	if d := m["VOO"].GetDayPct(); d != -0.33 {
		t.Errorf("VOO day %v, want rounded to -0.33", d)
	}
	if !m["BTC"].GetCrypto() || !near(m["BTC"].GetTotalPct(), 120) || m["BTC"].GetDayPct() != 2.5 {
		t.Errorf("BTC %v", m["BTC"])
	}
}

func TestBuildEmpty(t *testing.T) {
	st := build(asOf, nil, nil, nil, asOf)
	valid(t, st)
	if len(st.GetHoldings()) != 0 {
		t.Errorf("holdings from nothing: %v", st.GetHoldings())
	}
}

// Nothing in the state may be money: the schema has no field for it, and
// this keeps a later field from sneaking one in by name.
func TestNoMoneyFields(t *testing.T) {
	fields := (&pb.Holding{}).ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		name := string(fields.Get(i).Name())
		for _, bad := range []string{"price", "value", "units", "shares", "cost", "amount", "usd", "dollar"} {
			if strings.Contains(name, bad) {
				t.Errorf("Holding.%s looks like money", name)
			}
		}
	}
}

func TestParsePositions(t *testing.T) {
	b, err := os.ReadFile("testdata/positions.json")
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Results list[struct {
			Instrument struct {
				Kind   string `json:"kind"`
				Symbol string `json:"symbol"`
			} `json:"instrument"`
			Units     num  `json:"units"`
			Price     num  `json:"price"`
			CostBasis num  `json:"cost_basis"`
			Cash      bool `json:"cash_equivalent"`
		}] `json:"results"`
	}
	if err := json.Unmarshal(b, &reply); err != nil {
		t.Fatal(err)
	}
	r := reply.Results
	if len(r) != 6 || r[0].Units != 10 || r[0].Price != 200 || r[0].CostBasis != 150 {
		t.Fatalf("first row %+v", r[0])
	}
	if r[1].CostBasis != 0 || r[2].Price != 100000 || !r[4].Cash {
		t.Errorf("null, bare-number or cash rows misread: %+v %+v %+v", r[1], r[2], r[4])
	}
	if held(r[3].Instrument.Kind) || !held(r[2].Instrument.Kind) {
		t.Errorf("held: option shown or crypto hidden")
	}
}

func TestList(t *testing.T) {
	var a, b list[struct {
		ID string `json:"id"`
	}]
	if err := json.Unmarshal([]byte(`[{"id":"x"}]`), &a); err != nil || len(a) != 1 || a[0].ID != "x" {
		t.Errorf("bare list: %v %v", a, err)
	}
	if err := json.Unmarshal([]byte(`{"results":[{"id":"y"}]}`), &b); err != nil || len(b) != 1 || b[0].ID != "y" {
		t.Errorf("wrapped list: %v %v", b, err)
	}
}

func TestClip(t *testing.T) {
	if got := clip("Société", 5); got != "Soci" {
		t.Errorf("clip cut a rune: %q", got)
	}
}
