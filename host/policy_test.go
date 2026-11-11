package host

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

func TestHostPatterns(t *testing.T) {
	cases := []struct {
		pattern, host, port string
		want                bool
	}{
		{"api.example.com", "api.example.com", "443", true},
		{"api.example.com", "API.example.com", "443", false}, // callers lowercase first
		{"api.example.com", "evil.api.example.com", "443", false},
		{"*.example.com", "a.example.com", "443", true},
		{"*.example.com", "a.b.example.com", "443", false}, // exactly one label
		{"*.example.com", "example.com", "443", false},
		{"api.example.com", "api.example.com", "8443", false}, // non-default port must be named
		{"api.example.com:8443", "api.example.com", "8443", true},
		{"api.example.com:8443", "api.example.com", "443", false},
	}
	for _, tc := range cases {
		hp, err := parseHostPattern(tc.pattern)
		if err != nil {
			t.Fatal(err)
		}
		if got := hp.matches(tc.host, tc.port); got != tc.want {
			t.Errorf("%s matches %s:%s = %v, want %v", tc.pattern, tc.host, tc.port, got, tc.want)
		}
	}
	for _, bad := range []string{"", "*", "*.com", "10.0.0.1", "localhost", "a.example.com/path", "*.*.example.com"} {
		if _, err := parseHostPattern(bad); err == nil {
			t.Errorf("pattern %q should be refused", bad)
		}
	}
}

func TestPolicyIsTheIntersection(t *testing.T) {
	req := widget.NetworkRequest{Hosts: []string{"a.example.com", "b.example.com"}, Methods: []string{"GET", "POST"}}
	p, err := newEffectivePolicy(req, NetworkPolicy{Hosts: []string{"b.example.com", "c.example.com"}, Methods: []string{"get", "DELETE"}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(method, host string) error {
		return p.check(method, "https://"+host+"/", "https", host, "443")
	}
	if err := check("GET", "b.example.com"); err != nil {
		t.Errorf("requested and granted: %v", err)
	}
	if err := check("GET", "a.example.com"); err == nil {
		t.Error("requested but not granted should be refused")
	}
	if err := check("GET", "c.example.com"); err == nil {
		t.Error("granted but never requested should be refused: a grant can't widen")
	}
	if err := check("POST", "b.example.com"); err == nil {
		t.Error("a method not granted should be refused")
	}
	if err := check("DELETE", "b.example.com"); err == nil {
		t.Error("a method granted but never requested should be refused")
	}
	if err := p.check("GET", "http://b.example.com/", "http", "b.example.com", "80"); err == nil {
		t.Error("plain http should be refused by default")
	}
	if got := p.imageSources(); len(got) != 1 || got[0] != "https://b.example.com" {
		t.Errorf("img-src = %v", got)
	}

	off, _ := newEffectivePolicy(req, NetworkPolicy{Disabled: true})
	if err := off.check("GET", "x", "https", "a.example.com", "443"); err == nil || off.imageSources() != nil {
		t.Error("a disabled policy should refuse everything")
	}
	def, _ := newEffectivePolicy(widget.NetworkRequest{Hosts: []string{"a.example.com"}}, NetworkPolicy{})
	if err := def.check("POST", "x", "https", "a.example.com", "443"); err == nil {
		t.Error("a request naming no methods should get GET only")
	}
}

func TestForbiddenAddresses(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.10", "169.254.169.254",
		"100.101.102.103", "fd7a:115c:a1e0::1", "fe80::1", "0.0.0.0", "::", "::ffff:192.168.1.1",
		"fc00::1", "224.0.0.1", "255.255.255.255",
	} {
		if forbiddenAddr(netip.MustParseAddr(s)) == "" {
			t.Errorf("%s should be forbidden", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "140.82.112.3", "2606:4700::1111"} {
		if why := forbiddenAddr(netip.MustParseAddr(s)); why != "" {
			t.Errorf("%s forbidden: %s", s, why)
		}
	}
}

// publicTLS is a TLS server on loopback that the transport reaches as if it
// were any of `hosts` on port 443. allowLoopback controls whether the
// guarded dialer lets the connection through, so the same setup tests both
// the policy (allowed) and the hard rules (refused).
func publicTLS(t *testing.T, h http.Handler, allowLoopback bool) http.RoundTripper {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	var allow func(netip.Addr) bool
	if allowLoopback {
		allow = func(a netip.Addr) bool { return a.IsLoopback() }
	}
	d := guardedDialer(allow)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		// Every name "resolves" to the test server, as a rebinding attack
		// would make a public name resolve to loopback.
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	return tr
}

func newTestFetcher(t *testing.T, tr http.RoundTripper, req widget.NetworkRequest, grant NetworkPolicy) *fetcher {
	t.Helper()
	p, err := newEffectivePolicy(req, grant)
	if err != nil {
		t.Fatal(err)
	}
	return newFetcher(p, tr)
}

func get(t *testing.T, f *fetcher, url string, hdr ...string) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, http.NoBody)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return f.do(context.Background(), req, "")
}

func denied(err error) string {
	var pd *PolicyDeniedError
	if errors.As(err, &pd) {
		return pd.Rule
	}
	return ""
}

func TestFetchThroughPolicy(t *testing.T) {
	var sawCookie, sawAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		sawCookie, sawAuth = r.Header.Get("Cookie"), r.Header.Get("Authorization")
		io.WriteString(w, "hello")
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, 2048)) })
	mux.HandleFunc("/to-denied", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.com/ok", http.StatusFound)
	})
	mux.HandleFunc("/to-allowed", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://api.example.com/ok", http.StatusFound)
	})
	tr := publicTLS(t, mux, true)
	f := newTestFetcher(t, tr, widget.NetworkRequest{Hosts: []string{"api.example.com"}}, NetworkPolicy{MaxResponseBytes: 1024, RequestsPerMinute: 600})

	resp, err := get(t, f, "https://api.example.com/ok", "Cookie", "session=stolen", "Authorization", "Bearer x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}
	if sawCookie != "" || sawAuth != "" {
		t.Fatalf("host-owned headers reached the server: cookie=%q auth=%q", sawCookie, sawAuth)
	}

	if _, err := get(t, f, "https://other.example.com/ok"); denied(err) == "" {
		t.Fatalf("a host outside the request: %v", err)
	}
	if _, err := get(t, f, "https://api.example.com/to-denied"); denied(err) == "" {
		t.Fatalf("a redirect to a denied host should be refused: %v", err)
	}
	if resp, err := get(t, f, "https://api.example.com/to-allowed"); err != nil {
		t.Fatalf("a redirect within the policy: %v", err)
	} else {
		resp.Body.Close()
	}

	resp, err = get(t, f, "https://api.example.com/big")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("an oversized body should be cut off: %v", err)
	}

	acts := f.Activity()
	if len(acts) < 5 || acts[1].Denied == "" || acts[0].Status != 200 || strings.Contains(acts[0].Path, "?") {
		t.Fatalf("activity = %+v", acts)
	}
}

func TestRebindingToLoopbackIsRefused(t *testing.T) {
	tr := publicTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request must never reach a loopback server")
	}), false)
	f := newTestFetcher(t, tr, widget.NetworkRequest{Hosts: []string{"api.example.com"}}, NetworkPolicy{})
	_, err := get(t, f, "https://api.example.com/ok")
	if rule := denied(err); rule != "loopback address" {
		t.Fatalf("rule = %q (err %v), want loopback address", rule, err)
	}
}

func TestRateLimit(t *testing.T) {
	tr := publicTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), true)
	f := newTestFetcher(t, tr, widget.NetworkRequest{Hosts: []string{"api.example.com"}}, NetworkPolicy{RequestsPerMinute: 6})
	var refused int
	for range 5 {
		resp, err := get(t, f, "https://api.example.com/")
		if err != nil {
			if denied(err) == "" {
				t.Fatal(err)
			}
			refused++
			continue
		}
		resp.Body.Close()
	}
	if refused == 0 {
		t.Fatal("bursting past the per-minute limit should be refused")
	}
}

func TestFrameCSPHasNoConnections(t *testing.T) {
	csp := frameCSP("https://h/widgetd/v/w/d/", []string{"https://img.example.com"})
	for _, want := range []string{"connect-src 'none'", "img-src https://h/widgetd/v/w/d/ data: https://img.example.com", "default-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
}
