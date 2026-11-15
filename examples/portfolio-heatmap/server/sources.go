package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	snaptradeAPI = "https://api.snaptrade.com"
	finnhubAPI   = "https://finnhub.io/api/v1"
	coinbaseAPI  = "https://api.exchange.coinbase.com"
	userAgent    = "widgetd-portfolio-heatmap"
)

// errRateLimited is a quote source asking us to slow down.
var errRateLimited = errors.New("rate limited")

// num reads a JSON number that may come as a string ("12.5"), a number or
// null. A missing or unparsable value is 0.
type num float64

// UnmarshalJSON implements json.Unmarshaler.
func (n *num) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if len(b) == 0 || string(b) == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		*n = 0
		return nil //nolint:nilerr // a field we can't read counts as unknown, not as a broken reply
	}
	*n = num(f)
	return nil
}

// readPositions reads every position in every connected account.
func readPositions(ctx context.Context, h widget.Host) ([]position, error) {
	var accounts list[struct {
		ID string `json:"id"`
	}]
	if err := getJSON(ctx, h, snaptradeAPI+"/accounts/all", true, &accounts); err != nil {
		return nil, err
	}
	var out []position
	for _, a := range accounts {
		var reply struct {
			Results []struct {
				Instrument struct {
					Kind        string `json:"kind"`
					Symbol      string `json:"symbol"`
					Description string `json:"description"`
				} `json:"instrument"`
				Units          num  `json:"units"`
				Price          num  `json:"price"`
				CostBasis      num  `json:"cost_basis"`
				CashEquivalent bool `json:"cash_equivalent"`
			} `json:"results"`
		}
		path := snaptradeAPI + "/accounts/" + url.PathEscape(a.ID) + "/positions/all"
		if err := getJSON(ctx, h, path, true, &reply); err != nil {
			return nil, err
		}
		for _, p := range reply.Results {
			kind := strings.ToLower(p.Instrument.Kind)
			if p.CashEquivalent || !held(kind) {
				continue
			}
			out = append(out, position{
				Symbol: strings.ToUpper(strings.TrimSpace(p.Instrument.Symbol)),
				Name:   strings.TrimSpace(p.Instrument.Description),
				Crypto: kind == "crypto",
				Units:  float64(p.Units),
				Price:  float64(p.Price),
				Cost:   float64(p.CostBasis),
			})
		}
	}
	return out, nil
}

// held is whether the card shows an instrument of this kind: shares, funds
// and coins. Options, bonds and the like are left off.
func held(kind string) bool {
	switch kind {
	case "stock", "etf", "mutualfund", "cef", "adr", "preferred_stock", "crypto", "":
		return true
	}
	return false
}

// finnhubQuote reads a stock or fund's price and change today, signed
// with the display's Finnhub key by the host. ok is false when Finnhub
// doesn't know the symbol, or the free tier doesn't cover it.
func finnhubQuote(ctx context.Context, h widget.Host, symbol string) (q quote, ok bool, err error) {
	var r struct {
		C  num `json:"c"`
		DP num `json:"dp"`
		PC num `json:"pc"`
	}
	err = getJSON(ctx, h, finnhubAPI+"/quote?symbol="+url.QueryEscape(symbol), true, &r)
	var fe *forbiddenError
	if errors.As(err, &fe) {
		return quote{}, false, nil
	}
	if err != nil {
		return quote{}, false, err
	}
	if r.C <= 0 || r.PC <= 0 {
		return quote{}, false, nil
	}
	return quote{Price: float64(r.C), DayPct: float64(r.DP)}, true, nil
}

// coinbaseQuote reads a coin's price and its change over 24 hours, in US
// dollars. ok is false when Coinbase has no USD market for it.
func coinbaseQuote(ctx context.Context, h widget.Host, symbol string) (q quote, ok bool, err error) {
	base, _, _ := strings.Cut(symbol, "-")
	var r struct {
		Open num `json:"open"`
		Last num `json:"last"`
	}
	err = getJSON(ctx, h, coinbaseAPI+"/products/"+url.PathEscape(base+"-USD")+"/stats", false, &r)
	var nf *notFoundError
	if errors.As(err, &nf) {
		return quote{}, false, nil
	}
	if err != nil {
		return quote{}, false, err
	}
	if r.Open <= 0 || r.Last <= 0 {
		return quote{}, false, nil
	}
	return quote{Price: float64(r.Last), DayPct: float64((r.Last - r.Open) / r.Open * 100)}, true, nil
}

// notFoundError is a 404 from a quote source.
type notFoundError struct{ url string }

func (e *notFoundError) Error() string { return e.url + ": not found" }

// forbiddenError is a 403: for Finnhub, a symbol its free tier doesn't
// cover. (A key it refuses is a 401, which the host handles: it signs the
// account out and asks for it again.)
type forbiddenError struct{ url string }

func (e *forbiddenError) Error() string { return e.url + ": forbidden" }

// getJSON GETs u and decodes its JSON reply into into. With signed, the
// host signs the request for the display's account with the API's
// provider (SnapTrade, Finnhub).
func getJSON(ctx context.Context, h widget.Host, u string, signed bool, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if signed {
		req = widget.Authorize(req)
	}
	resp, err := h.Fetch(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close() // read-only; nothing to lose
	}()
	where := redact(u)
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		return nil
	case http.StatusNotFound:
		return &notFoundError{url: where}
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s: %w", where, errRateLimited)
	case http.StatusForbidden:
		return &forbiddenError{url: where}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return fmt.Errorf("%s: %s: %s", where, resp.Status, strings.TrimSpace(string(body)))
}

// redact drops a URL's query, so logs never carry anything passed in it.
func redact(u string) string {
	base, _, _ := strings.Cut(u, "?")
	return base
}

// list reads a JSON list given either bare or as {"results": [...]}.
type list[T any] []T

// UnmarshalJSON implements json.Unmarshaler.
func (l *list[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		return json.Unmarshal(b, (*[]T)(l))
	}
	var wrapped struct {
		Results []T `json:"results"`
	}
	if err := json.Unmarshal(b, &wrapped); err != nil {
		return err
	}
	*l = wrapped.Results
	return nil
}
