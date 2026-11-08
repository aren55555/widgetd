package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

// NetworkPolicy is what the host grants a widget. The effective policy is
// the intersection of this and the widget's own NetworkRequest: a grant can
// narrow a request, never widen it.
type NetworkPolicy struct {
	// Disabled cuts the widget off from the network entirely.
	Disabled bool
	// Hosts granted, as patterns like the request's. Nil grants every host
	// the widget asked for.
	Hosts []string
	// Methods granted. Nil grants every method the widget asked for.
	Methods []string
	// RequestsPerMinute per instance. Zero means the default (60).
	RequestsPerMinute int
	// MaxResponseBytes per response. Zero means the default (4 MiB).
	MaxResponseBytes int64
	// Timeout per request, redirects included. Zero means the default (15s).
	Timeout time.Duration
	// AllowHTTP permits plain http:// to granted hosts. Off by default.
	AllowHTTP bool
}

const (
	defaultRequestsPerMinute = 60
	defaultMaxResponseBytes  = 4 << 20
	defaultFetchTimeout      = 15 * time.Second
	maxRedirects             = 5
	activityKept             = 50
)

// PolicyDenied is the error a widget's fetch fails with when the policy
// refuses it. Rule names the rule, so a too-tight policy reads as a policy
// problem rather than a broken widget.
type PolicyDenied struct {
	Rule string
	URL  string
}

func (e *PolicyDenied) Error() string {
	return fmt.Sprintf("network policy: %s refused (%s)", e.URL, e.Rule)
}

// effectivePolicy is a request intersected with a grant, ready to check.
type effectivePolicy struct {
	disabled  bool
	request   []hostPattern
	grant     []hostPattern // nil: whatever the request allows
	methods   []string
	allowHTTP bool
	maxBytes  int64
	timeout   time.Duration
	perMinute int
}

func newEffectivePolicy(req widget.NetworkRequest, grant NetworkPolicy) (*effectivePolicy, error) {
	p := &effectivePolicy{
		disabled:  grant.Disabled,
		allowHTTP: grant.AllowHTTP,
		maxBytes:  cmpOr(grant.MaxResponseBytes, defaultMaxResponseBytes),
		timeout:   cmpOr(grant.Timeout, defaultFetchTimeout),
		perMinute: cmpOr(grant.RequestsPerMinute, defaultRequestsPerMinute),
	}
	for _, h := range req.Hosts {
		hp, err := parseHostPattern(h)
		if err != nil {
			return nil, fmt.Errorf("network request: %w", err)
		}
		p.request = append(p.request, hp)
	}
	if grant.Hosts != nil {
		p.grant = []hostPattern{}
		for _, h := range grant.Hosts {
			hp, err := parseHostPattern(h)
			if err != nil {
				return nil, fmt.Errorf("network grant: %w", err)
			}
			p.grant = append(p.grant, hp)
		}
	}
	reqMethods := upper(req.Methods)
	if len(reqMethods) == 0 {
		reqMethods = []string{http.MethodGet}
	}
	p.methods = reqMethods
	if grant.Methods != nil {
		granted := upper(grant.Methods)
		p.methods = slices.DeleteFunc(slices.Clone(reqMethods), func(m string) bool { return !slices.Contains(granted, m) })
	}
	return p, nil
}

func cmpOr[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

func upper(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToUpper(s)
	}
	return out
}

// hostPattern is "host", "*.domain" (exactly one more label), with an
// optional port.
type hostPattern struct {
	wildcard bool
	host     string // without "*.", lowercase
	port     string // "" for the scheme's default
}

func parseHostPattern(s string) (hostPattern, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	var hp hostPattern
	if h, p, err := net.SplitHostPort(s); err == nil {
		s, hp.port = h, p
	}
	if rest, ok := strings.CutPrefix(s, "*."); ok {
		hp.wildcard, s = true, rest
	}
	if s == "" || strings.ContainsAny(s, "*/:@ ") || !strings.Contains(s, ".") {
		return hostPattern{}, fmt.Errorf("host pattern %q: want host.example or *.example.com", s)
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return hostPattern{}, fmt.Errorf("host pattern %q: name a host, not an IP address", s)
	}
	hp.host = s
	return hp, nil
}

func (hp hostPattern) matches(host, port string) bool {
	if hp.port != "" && hp.port != port {
		return false
	}
	if hp.port == "" && port != "443" && port != "80" {
		return false
	}
	if hp.wildcard {
		label, rest, ok := strings.Cut(host, ".")
		return ok && label != "" && rest == hp.host
	}
	return host == hp.host
}

func anyMatch(ps []hostPattern, host, port string) bool {
	return slices.ContainsFunc(ps, func(p hostPattern) bool { return p.matches(host, port) })
}

// check decides one request (or redirect hop) by URL and method.
func (p *effectivePolicy) check(method, rawURL string, scheme, host, port string) error {
	deny := func(rule string) error { return &PolicyDenied{Rule: rule, URL: rawURL} }
	switch {
	case p.disabled:
		return deny("network disabled for this widget")
	case scheme != "https" && !(scheme == "http" && p.allowHTTP):
		return deny("only https is allowed")
	case !slices.Contains(p.methods, method):
		return deny("method " + method + " not granted")
	case !anyMatch(p.request, host, port):
		return deny("host not in the widget's network request")
	case p.grant != nil && !anyMatch(p.grant, host, port):
		return deny("host not granted")
	}
	return nil
}

// imageSources is the CSP img-src list for the widget's cards: the hosts it
// may fetch from, so a view can show (say) a logo from its own API, and
// nothing else.
func (p *effectivePolicy) imageSources() []string {
	if p.disabled {
		return nil
	}
	var out []string
	for _, r := range p.request {
		if p.grant != nil && !slices.ContainsFunc(p.grant, func(g hostPattern) bool { return g == r }) {
			continue
		}
		h := r.host
		if r.wildcard {
			h = "*." + h
		}
		if r.port != "" {
			h += ":" + r.port
		}
		out = append(out, "https://"+h)
	}
	return out
}

// ---- the hard rules: addresses a widget may never reach ----

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",           // "this network"
		"100.64.0.0/10",       // CGNAT, which Tailscale uses for tailnet addresses
		"192.0.0.0/24",        // IETF protocol assignments
		"192.0.2.0/24",        // documentation
		"198.18.0.0/15",       // benchmarking
		"198.51.100.0/24",     // documentation
		"203.0.113.0/24",      // documentation
		"240.0.0.0/4",         // reserved, broadcast
		"64:ff9b::/96",        // NAT64, which can reach IPv4 private space
		"2001:db8::/32",       // documentation
		"fd7a:115c:a1e0::/48", // Tailscale's IPv6 range
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// forbiddenAddr reports why a widget may not connect to ip, or "".
func forbiddenAddr(ip netip.Addr) string {
	ip = ip.Unmap()
	switch {
	case !ip.IsValid():
		return "invalid address"
	case ip.IsLoopback():
		return "loopback address"
	case ip.IsPrivate():
		return "private address"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast():
		return "link-local address" // includes 169.254.169.254, cloud metadata
	case ip.IsUnspecified(), ip.IsMulticast():
		return "non-unicast address"
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return "reserved or tailnet address"
		}
	}
	return ""
}

// guardedDialer refuses, at connect time, any address forbiddenAddr names.
// Checking the address actually being connected to (after DNS) is what
// stops a hostname that resolves, or re-resolves, to the LAN.
func guardedDialer(allow func(netip.Addr) bool) *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("dial %s: %w", address, err)
			}
			if allow != nil && allow(ap.Addr()) {
				return nil
			}
			if why := forbiddenAddr(ap.Addr()); why != "" {
				return &PolicyDenied{Rule: why, URL: address}
			}
			return nil
		},
	}
}

func newGuardedTransport(allow func(netip.Addr) bool) *http.Transport {
	d := guardedDialer(allow)
	return &http.Transport{
		Proxy:                 nil, // never a proxy from the environment
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
}

// ---- per-instance fetcher ----

// Activity is one fetch an instance made or was refused.
type Activity struct {
	At       time.Time
	Method   string
	Host     string
	Path     string // no query: it may carry secrets
	Status   int
	Bytes    int64
	Denied   string // the rule, if refused
	Err      string
	Duration time.Duration
}

// fetcher performs one instance's requests under its policy, and keeps a
// short record of them.
type fetcher struct {
	policy    atomic.Pointer[effectivePolicy]
	transport http.RoundTripper
	limiter   *rate.Limiter

	mu       sync.Mutex
	activity []Activity
}

func newFetcher(p *effectivePolicy, transport http.RoundTripper) *fetcher {
	f := &fetcher{transport: transport, limiter: rate.NewLimiter(rate.Limit(float64(p.perMinute)/60), max(1, p.perMinute/6))}
	f.policy.Store(p)
	return f
}

// setPolicy swaps in a new policy; the next request is checked against it.
func (f *fetcher) setPolicy(p *effectivePolicy) {
	f.policy.Store(p)
	f.limiter.SetLimit(rate.Limit(float64(p.perMinute) / 60))
	f.limiter.SetBurst(max(1, p.perMinute/6))
}

// headers a widget may not set: the host owns them.
var hostOwnedHeaders = []string{"Host", "Cookie", "Authorization", "Proxy-Authorization", "Proxy-Connection", "Connection", "Upgrade", "Te", "Trailer", "Transfer-Encoding", "Forwarded", "Widgetd-Authorize", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"}

func (f *fetcher) record(a Activity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activity = append(f.activity, a)
	if len(f.activity) > activityKept {
		f.activity = f.activity[len(f.activity)-activityKept:]
	}
}

// Activity returns the most recent fetches, oldest first.
func (f *fetcher) Activity() []Activity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.activity)
}

// do performs a widget's request. auth, if set, is the Authorization the
// host adds for a signed-in widget: after the widget's own headers are
// stripped, so a widget can never set one itself.
func (f *fetcher) do(ctx context.Context, req *http.Request, auth string) (*http.Response, error) {
	start := time.Now()
	act := Activity{At: start, Method: req.Method, Host: req.URL.Host, Path: req.URL.Path}
	fail := func(err error) (*http.Response, error) {
		var pd *PolicyDenied
		if errors.As(err, &pd) {
			act.Denied = pd.Rule
		} else {
			act.Err = err.Error()
		}
		act.Duration = time.Since(start)
		f.record(act)
		return nil, err
	}

	if err := f.checkURL(req.Method, req); err != nil {
		return fail(err)
	}
	p := f.policy.Load()
	if !f.limiter.Allow() {
		return fail(&PolicyDenied{Rule: fmt.Sprintf("over %d requests per minute", p.perMinute), URL: req.URL.Redacted()})
	}

	out := req.Clone(ctx)
	out.RequestURI = ""
	for _, h := range hostOwnedHeaders {
		out.Header.Del(h)
	}
	if auth != "" {
		out.Header.Set("Authorization", auth)
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	client := &http.Client{
		Transport: f.transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return &PolicyDenied{Rule: "too many redirects", URL: next.URL.Redacted()}
			}
			return f.checkURL(next.Method, next)
		},
	}
	resp, err := client.Do(out.WithContext(ctx))
	if err != nil {
		cancel()
		// A refusal from the dialer or a redirect check arrives wrapped in a
		// *url.Error; hand the widget the PolicyDenied itself.
		var pd *PolicyDenied
		if errors.As(err, &pd) {
			return fail(pd)
		}
		return fail(err)
	}
	act.Status = resp.StatusCode
	resp.Body = &cappedBody{
		r: io.LimitReader(resp.Body, p.maxBytes+1), c: resp.Body, limit: p.maxBytes,
		done: func(n int64, err error) {
			cancel()
			act.Bytes, act.Duration = n, time.Since(start)
			if err != nil {
				act.Err = err.Error()
			}
			f.record(act)
		},
	}
	return resp, nil
}

func (f *fetcher) checkURL(method string, req *http.Request) error {
	u := req.URL
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return f.policy.Load().check(method, u.Redacted(), u.Scheme, strings.ToLower(u.Hostname()), port)
}

var errBodyTooLarge = errors.New("response body over the policy's size limit")

// cappedBody stops a response at the policy's size limit and records the
// fetch once the widget is done with it.
type cappedBody struct {
	r     io.Reader
	c     io.Closer
	n     int64
	limit int64
	err   error
	once  sync.Once
	done  func(n int64, err error)
}

func (b *cappedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.n > b.limit {
		b.err = errBodyTooLarge
		return n, errBodyTooLarge
	}
	return n, err
}

func (b *cappedBody) Close() error {
	err := b.c.Close()
	b.once.Do(func() { b.done(b.n, b.err) })
	return err
}
