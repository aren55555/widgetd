package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

// fakeHost answers Fetch with one canned body, and keeps the URL it was asked for.
type fakeHost struct {
	widget.Host
	body string
	url  string
}

func (f *fakeHost) Fetch(req *http.Request) (*http.Response, error) {
	f.url = req.URL.String()
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(f.body))}, nil
}

func TestCoinbaseMonth(t *testing.T) {
	// Newest first, as Coinbase sends them; one row short, one repeated.
	h := &fakeHost{body: `[
		[1763078400, 1, 2, 1, 105.5, 9],
		[1762992000, 1, 2, 1, 104, 9],
		[1762992000, 1, 2, 1, 104, 9],
		[1762905600, 1, 2],
		[1762819200, 1, 2, 1, 100, 9]
	]`}
	now := time.Date(2025, 11, 14, 12, 0, 0, 0, time.UTC)
	pts, err := coinbaseMonth(context.Background(), h, "BTC", now)
	if err != nil {
		t.Fatal(err)
	}
	want := []point{{"2025-11-11", 100}, {"2025-11-13", 104}, {"2025-11-14", 105.5}}
	if !slices.Equal(pts, want) {
		t.Errorf("got %v, want %v", pts, want)
	}
	if !strings.Contains(h.url, "/products/BTC-USD/candles?granularity=86400&start=2025-10-14") {
		t.Errorf("asked for %s", h.url)
	}
}

func TestFinnhubQuoteDays(t *testing.T) {
	// The last trade: Monday 16 Nov 2026, 15:59 Eastern.
	last := time.Date(2026, 11, 16, 20, 59, 0, 0, time.UTC).Unix()
	h := &fakeHost{body: fmt.Sprintf(`{"c": 110, "dp": 1.5, "pc": 108.4, "t": %d}`, last)}
	q, ok, err := finnhubQuote(context.Background(), h, "AAPL")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if q.Day != "2026-11-16" || q.PrevDay != "2026-11-13" || q.PrevClose != 108.4 || q.Price != 110 {
		t.Errorf("got %+v", q)
	}
}
