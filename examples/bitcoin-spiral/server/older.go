package server

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/aren55555/widgetd/examples/bitcoin-spiral/gen/bitcoinspiral/v1"
)

// Older history: days before Coinbase's daily data begins (mid 2015), from
// a source chosen in the instance's config. It is fetched once and kept
// under its own key, apart from the Coinbase history, so switching source
// (or turning it off) never mixes or loses either.

const (
	bitstampAPI   = "https://www.bitstamp.net/api/v2"
	blockchainAPI = "https://api.blockchain.info"
	bitstampPage  = 1000
	bitstampFirst = 1313625600 // 2011-08-18, Bitstamp's first BTC/USD day
	secondsPerDay = int64(day / time.Second)
)

func olderKey(src pb.OlderHistory) string { return "older:" + src.String() }

// loadOlder puts the configured older history in front of the daily
// closes, fetching it the first time. It returns how many days it added.
func (w *spiral) loadOlder(ctx context.Context) (int, error) {
	if w.older == pb.OlderHistory_OLDER_HISTORY_UNSPECIFIED || w.cur != pb.Currency_CURRENCY_USD {
		return 0, nil
	}
	var older []*pb.DailyClose
	b, ok, err := w.h.KV().Get(olderKey(w.older))
	if err != nil {
		return 0, err
	}
	if ok {
		var hist pb.History
		if err := proto.Unmarshal(b, &hist); err != nil {
			return 0, err
		}
		older = hist.GetDaily()
	} else {
		switch w.older {
		case pb.OlderHistory_OLDER_HISTORY_BITSTAMP:
			older, err = w.bitstampCloses(ctx)
		case pb.OlderHistory_OLDER_HISTORY_BLOCKCHAIN_INFO:
			older, err = w.blockchainAverages(ctx)
		case pb.OlderHistory_OLDER_HISTORY_UNSPECIFIED:
			// No older source: the spiral starts where recent history does.
		}
		if err != nil {
			return 0, err
		}
		b, err := proto.Marshal(&pb.History{Daily: older})
		if err != nil {
			return 0, err
		}
		if err := w.h.KV().Put(olderKey(w.older), b); err != nil {
			return 0, err
		}
	}
	older = beforeCoinbase(older, firstDay[w.cur])
	w.state.Daily = append(older, w.state.Daily...)
	w.olderN = len(older)
	return len(older), nil
}

// beforeCoinbase keeps the days before Coinbase's first, oldest first, one
// per day, with a positive price.
func beforeCoinbase(days []*pb.DailyClose, first time.Time) []*pb.DailyClose {
	cut := int32(first.Unix() / secondsPerDay)
	var out []*pb.DailyClose
	for _, d := range days {
		if d.GetDay() < cut && d.GetClose() > 0 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetDay() < out[j].GetDay() })
	dedup := out[:0]
	for _, d := range out {
		if n := len(dedup); n == 0 || dedup[n-1].GetDay() != d.GetDay() {
			dedup = append(dedup, d)
		}
	}
	return dedup
}

// bitstampCloses pages through Bitstamp's daily candles up to Coinbase's
// first day.
func (w *spiral) bitstampCloses(ctx context.Context) ([]*pb.DailyClose, error) {
	stop := firstDay[pb.Currency_CURRENCY_USD].Unix()
	var out []*pb.DailyClose
	for start := int64(bitstampFirst); start < stop; {
		q := url.Values{"step": {"86400"}, "limit": {strconv.Itoa(bitstampPage)}, "start": {strconv.FormatInt(start, 10)}}
		var resp struct {
			Data struct {
				OHLC []struct {
					Timestamp string `json:"timestamp"`
					Close     string `json:"close"`
				} `json:"ohlc"`
			} `json:"data"`
		}
		if err := w.getJSON(ctx, bitstampAPI+"/ohlc/btcusd/?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		rows := resp.Data.OHLC
		if len(rows) == 0 {
			break
		}
		last := start
		for _, r := range rows {
			ts, err1 := strconv.ParseInt(r.Timestamp, 10, 64)
			c, err2 := strconv.ParseFloat(r.Close, 64)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("bitstamp: bad row %+v", r)
			}
			out = append(out, &pb.DailyClose{Day: int32(ts / secondsPerDay), Close: c})
			last = ts
		}
		start = last + secondsPerDay
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pagePause):
		}
	}
	return out, nil
}

// blockchainAverages reads blockchain.info's whole daily market-price
// series (an average across exchanges, not a close).
func (w *spiral) blockchainAverages(ctx context.Context) ([]*pb.DailyClose, error) {
	var resp struct {
		Values []struct {
			X int64   `json:"x"`
			Y float64 `json:"y"`
		} `json:"values"`
	}
	if err := w.getJSON(ctx, blockchainAPI+"/charts/market-price?timespan=all&format=json&sampled=false", &resp); err != nil {
		return nil, err
	}
	out := make([]*pb.DailyClose, 0, len(resp.Values))
	for _, v := range resp.Values {
		out = append(out, &pb.DailyClose{Day: int32(v.X / secondsPerDay), Close: v.Y})
	}
	return out, nil
}
