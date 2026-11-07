package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	fetchTimeout       = 15 * time.Second
	fetchMaxBody int64 = 4 << 20
)

// fetch performs a widget's HTTP request. This is the one way out of a
// widget, which is what makes it the place to enforce a network policy.
//
// TODO(M1b): per-widget policy (allowed hosts, methods, rate) and a dialer
// that refuses private, loopback and tailnet addresses after resolution.
func (s *Server) fetch(ctx context.Context, in *instance, req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("fetch %s: only https is allowed", req.URL.Redacted())
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	resp, err := s.httpClient.Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cappedBody{r: io.LimitReader(resp.Body, fetchMaxBody+1), c: resp.Body, cancel: cancel}
	return resp, nil
}

var errBodyTooLarge = errors.New("response body over the fetch limit")

type cappedBody struct {
	r      io.Reader
	c      io.Closer
	n      int64
	cancel context.CancelFunc
}

func (b *cappedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.n > fetchMaxBody {
		return n, errBodyTooLarge
	}
	return n, err
}

func (b *cappedBody) Close() error {
	b.cancel()
	return b.c.Close()
}
