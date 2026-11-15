package server

import (
	"math"
	"slices"
	"strings"
	"time"

	pb "github.com/aren55555/widgetd/examples/portfolio-heatmap/gen/portfolio/v1"
)

// maxHoldings is how many tiles a card carries, largest first; the rest are
// too small to see.
const maxHoldings = 300

// position is one holding in one account, as the brokerage reports it.
// Units, price and cost stay on the server: only weights and percentages
// leave it.
type position struct {
	Symbol string
	Name   string
	Crypto bool
	Units  float64
	// Price is the brokerage's last price; a fresher quote replaces it.
	Price float64
	// Cost is the cost basis per unit, or 0 if the brokerage gives none.
	Cost float64
}

// quote is a fresh price and the change today (crypto: over 24 hours).
type quote struct {
	Price  float64
	DayPct float64
}

// holding is a symbol summed over every account that holds it.
type holding struct {
	position
	cost      float64 // total cost basis
	costKnown bool
}

// build turns positions and quotes into the card: each symbol's weight in
// the portfolio, its change today and its total return. Short positions
// and anything worth nothing are left out.
func build(asOf time.Time, positions []position, quotes map[string]quote) *pb.State {
	bySym := map[string]*holding{}
	var order []string
	for _, p := range positions {
		if p.Units <= 0 || p.Symbol == "" {
			continue
		}
		h := bySym[p.Symbol]
		if h == nil {
			h = &holding{position: position{Symbol: p.Symbol, Name: p.Name, Crypto: p.Crypto, Price: p.Price}, costKnown: true}
			bySym[p.Symbol] = h
			order = append(order, p.Symbol)
		}
		h.Units += p.Units
		if p.Price > 0 {
			h.Price = p.Price
		}
		if h.Name == "" {
			h.Name = p.Name
		}
		if p.Cost > 0 {
			h.cost += p.Units * p.Cost
		} else {
			h.costKnown = false
		}
	}

	type row struct {
		h     *holding
		value float64
		q     quote
		hasQ  bool
	}
	rows := make([]row, 0, len(order))
	var total float64
	for _, s := range order {
		h := bySym[s]
		q, ok := quotes[s]
		price := h.Price
		if ok && q.Price > 0 {
			price = q.Price
		}
		v := h.Units * price
		if !(v > 0) || math.IsInf(v, 0) {
			continue
		}
		h.Price = price
		rows = append(rows, row{h: h, value: v, q: q, hasQ: ok})
		total += v
	}
	slices.SortStableFunc(rows, func(a, b row) int {
		switch {
		case a.value > b.value:
			return -1
		case a.value < b.value:
			return 1
		}
		return strings.Compare(a.h.Symbol, b.h.Symbol)
	})

	st := &pb.State{AsOfUnixMs: asOf.UnixMilli()}
	for _, r := range rows {
		if len(st.Holdings) == maxHoldings {
			break
		}
		bp := int32(math.Round(r.value / total * 10000))
		if bp <= 0 {
			continue
		}
		out := &pb.Holding{
			Symbol:   clip(r.h.Symbol, 24),
			Name:     clip(r.h.Name, 120),
			WeightBp: bp,
			Crypto:   r.h.Crypto,
		}
		if r.hasQ {
			out.DayPct = pct(r.q.DayPct, 100000)
		}
		if r.h.costKnown && r.h.cost > 0 {
			out.TotalPct = pct((r.value-r.h.cost)/r.h.cost*100, 1000000)
		}
		st.Holdings = append(st.Holdings, out)
	}
	return st
}

// pct rounds a percentage to 0.01 (so a tick that moves nothing visible
// sends nothing) and keeps it within the schema's range; nil if it isn't a
// number.
func pct(v, hi float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	v = math.Round(v*100) / 100
	v = min(max(v, -100), hi)
	return &v
}

// clip cuts s to at most n bytes, on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
