package host

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

func TestAPIKeyManifestChecks(t *testing.T) {
	base := `
name = "k"
title = "K"
[sign_in]
flow = "api_key"
key_in = "header:X-Api-Key"
revoke = "none"
[scopes.covers]
q = ["GET https://api.example.com/q"]
`
	p, err := parseProvider([]byte(base))
	if err != nil {
		t.Fatalf("a good manifest: %v", err)
	}
	if p.Flow != flowAPIKey || p.keyHeader != "X-Api-Key" || p.keyQuery != "" {
		t.Fatalf("parsed %+v", p)
	}
	q, err := parseProvider([]byte(strings.Replace(base, `key_in = "header:X-Api-Key"`, `key_in = "query:token"`, 1)))
	if err != nil || q.keyQuery != "token" || q.keyHeader != "" {
		t.Fatalf("a query key: %+v %v", q, err)
	}
	for name, m := range map[string]string{
		"no key_in":         strings.Replace(base, `key_in = "header:X-Api-Key"`, "", 1),
		"key_in nowhere":    strings.Replace(base, `key_in = "header:X-Api-Key"`, `key_in = "body:key"`, 1),
		"key_in no name":    strings.Replace(base, `key_in = "header:X-Api-Key"`, `key_in = "header:"`, 1),
		"key_in bad name":   strings.Replace(base, `key_in = "header:X-Api-Key"`, `key_in = "header:X Api"`, 1),
		"host-owned header": strings.Replace(base, `key_in = "header:X-Api-Key"`, `key_in = "header:cookie"`, 1),
		"authorization":     strings.Replace(base, `key_in = "header:X-Api-Key"`, `key_in = "header:Authorization"`, 1),
		"revokes":           strings.Replace(base, `revoke = "none"`, `revoke = "rfc7009"`+"\nrevoke_url = \"https://api.example.com/r\"", 1),
		"has an identity":   base + "[identity]\nurl = \"https://api.example.com/me\"\nfield = \"login\"\n",
		"key_in, not a key": strings.Replace(base, `flow = "api_key"`, `flow = "device"`+"\ndevice_url = \"https://x.example.com/d\"\ntoken_url = \"https://x.example.com/t\"", 1),
	} {
		if _, err := parseProvider([]byte(m)); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	// The shipped one.
	ps, err := loadBuiltinProviders()
	if err != nil {
		t.Fatal(err)
	}
	if f := ps["finnhub"]; f == nil || f.Flow != flowAPIKey || f.keyHeader != "X-Finnhub-Token" {
		t.Fatalf("finnhub: %+v", f)
	}
}

const finnhubHost = "finnhub.example.com"

// fakeFinnhub answers /api/v1/quote, and records the key each request
// carried. A request with the key "dead" gets a 401.
type fakeFinnhub struct {
	mu   sync.Mutex
	keys []string
}

func (f *fakeFinnhub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Finnhub-Token")
	f.mu.Lock()
	f.keys = append(f.keys, r.URL.Path+" "+key)
	f.mu.Unlock()
	switch {
	case key == "dead":
		http.Error(w, `{"error":"Invalid API key"}`, http.StatusUnauthorized)
	case r.URL.Path == "/api/v1/quote":
		fmt.Fprint(w, `{"c":101,"dp":1.5,"pc":99.5}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeFinnhub) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.keys)
}

// newKeyHarness: widget "wk" uses the shipped finnhub provider [quote]; its
// run fetches a quote every 50 ms with a forged key of its own, and
// reports the status and whether the key showed in the request it got
// back.
func newKeyHarness(t *testing.T) (*harness, *fakeFinnhub, chan string) {
	t.Helper()
	fh := &fakeFinnhub{}
	tr := publicTLS(t, fh, true)
	results := make(chan string, 256)
	spec := widget.Spec{
		Name: "wk", Version: "1",
		Files:  []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Config: &testwidgetv1.Config{},
		View:   testSpec(nil).View,
		Run: func(ctx context.Context, h widget.Host) error {
			for {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+finnhubHost+"/api/v1/quote?symbol=AAPL", http.NoBody)
				req.Header.Set("X-Finnhub-Token", "forged")
				resp, err := h.Fetch(widget.Authorize(req))
				if err != nil {
					results <- "err: " + err.Error()
				} else {
					_ = resp.Body.Close()
					results <- fmt.Sprintf("%s leaked=%v", resp.Status, resp.Request.Header.Get("X-Finnhub-Token") != "forged")
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
			}
		},
		Network: widget.NetworkRequest{Hosts: []string{finnhubHost}},
		Uses:    []widget.Use{{Provider: "finnhub", Scopes: []string{"quote"}}},
	}
	b, err := builtinProviderFS.ReadFile("providers/finnhub.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := parseProvider(bytes.ReplaceAll(b, []byte("finnhub.io"), []byte(finnhubHost)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "https://widgets.example.com", Widgets: []widget.Spec{spec},
		Instances: []Instance{{ID: "ik", Widget: "wk"}}, transport: tr, providers: map[string]*provider{p.Name: p}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	t.Cleanup(func() { h.http.Close(); srv.Close(context.Background()) })
	srv.Start()
	return h, fh, results
}

func (h *harness) keyState() widgetdv1.InstanceStatus_State {
	h.srv.mu.Lock()
	in := h.srv.instances["ik"]
	h.srv.mu.Unlock()
	return in.status().GetState()
}

func (h *harness) waitKeyState(want widgetdv1.InstanceStatus_State) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.keyState() != want {
		if time.Now().After(deadline) {
			h.t.Fatalf("state %v, want %v", h.keyState(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) setKey(key string) {
	h.t.Helper()
	if _, err := h.adminClient().SetProviderClient(context.Background(), withCookie(&widgetdv1.SetProviderClientRequest{
		Provider: "finnhub", ClientSecret: key}, h.adminCookie())); err != nil {
		h.t.Fatal(err)
	}
}

// keySignIn signs a new account in from the admin page, as the browser
// would: open the link, which finishes at once. It returns where the
// browser was sent.
func (h *harness) keySignIn() *url.URL {
	h.t.Helper()
	start, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "finnhub", InstanceId: "ik"}, h.adminCookie()))
	if err != nil {
		h.t.Fatal(err)
	}
	link, _ := url.Parse(start.Msg.GetUrl())
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(h.http.URL + link.Path)
	if err != nil {
		h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("opening the sign-in link: %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	return loc
}

func drain(c chan string) {
	for {
		select {
		case <-c:
		default:
			return
		}
	}
}

func TestAPIKeyAccount(t *testing.T) {
	h, fh, results := newKeyHarness(t)
	h.waitKeyState(NS)

	// No key yet: nothing to sign in with.
	if _, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "finnhub", InstanceId: "ik"}, h.adminCookie())); err == nil {
		t.Fatal("signing in before the key is set should fail")
	}
	// The key alone, no client ID; the admin page is told it's key-only.
	h.setKey("k1")
	list, err := h.adminClient().ListAccounts(context.Background(), withCookie(&widgetdv1.ListAccountsRequest{}, h.adminCookie()))
	if err != nil {
		t.Fatal(err)
	}
	if ps := list.Msg.GetProviders(); len(ps) != 1 || !ps[0].GetKeyOnly() || !ps[0].GetSecretSet() || ps[0].GetSecretLabel() != "API key" {
		t.Fatalf("providers: %v", ps)
	}

	loc := h.keySignIn()
	id := loc.Query().Get("name")
	if id == "" {
		t.Fatalf("a new account should be sent to be named: %s", loc)
	}
	a, err := h.srv.loadAcct(id)
	if err != nil || a == nil || a.Token == nil || a.Token.Access != "" || !slices.Equal(a.Token.Granted, []string{"quote"}) {
		t.Fatalf("account: %+v %v", a, err)
	}
	if _, err := h.adminClient().LabelAccount(context.Background(), withCookie(&widgetdv1.LabelAccountRequest{AccountId: id, Label: "Finnhub"}, h.adminCookie())); err != nil {
		t.Fatal(err)
	}
	h.waitKeyState(RUN)

	// The request carries the key, not the widget's own, and what the
	// widget gets back doesn't show it.
	select {
	case r := <-results:
		if r != "200 OK leaked=false" {
			t.Fatalf("widget fetch: %s", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the widget never fetched")
	}
	if seen := fh.seen(); len(seen) == 0 || seen[0] != "/api/v1/quote k1" {
		t.Fatalf("Finnhub saw %v", seen)
	}

	// Only what the scope covers is signed.
	in := h.srv.instances["ik"]
	for _, c := range []struct{ method, raw string }{
		{"GET", "https://" + finnhubHost + "/api/v1/stock/profile2"},
		{"GET", "https://" + finnhubHost + "/api/v1/quotes"},
		{"POST", "https://" + finnhubHost + "/api/v1/quote"},
	} {
		if _, _, _, err := h.srv.authorization(context.Background(), in, c.method, mustURL(t, c.raw)); err == nil {
			t.Errorf("signed %s %s", c.method, c.raw)
		}
	}

	// A new key takes effect at once, with no new sign-in.
	h.setKey("k2")
	drain(results)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(fh.seen(), "/api/v1/quote k2") {
		if time.Now().After(deadline) {
			t.Fatalf("the new key was never used: %v", fh.seen())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A key Finnhub refuses signs the account out, and the widget waits.
	h.setKey("dead")
	h.waitKeyState(NS)
	if a, _ := h.srv.loadAcct(id); a == nil || a.Token != nil {
		t.Fatalf("after a 401 the account should be signed out: %+v", a)
	}
	// A good key, and a sign-in again, puts it back.
	h.setKey("k3")
	start, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "finnhub", AccountId: id}, h.adminCookie()))
	if err != nil {
		t.Fatal(err)
	}
	link, _ := url.Parse(start.Msg.GetUrl())
	resp, err := http.Get(h.http.URL + link.Path)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	h.waitKeyState(RUN)
}

// The headers a signer sets don't follow a redirect to another host; they
// do follow one on the same host.
func TestSignedHeadersStayOnTheirHost(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	tr := publicTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.Host+r.URL.Path] = r.Header.Get("X-Key")
		mu.Unlock()
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, "https://b.example.com/landed", http.StatusFound)
		case "/home":
			http.Redirect(w, r, "/landed", http.StatusFound)
		}
	}), true)
	f := newTestFetcher(t, tr, widget.NetworkRequest{Hosts: []string{"a.example.com", "b.example.com"}}, NetworkPolicy{})
	sign := func(r *http.Request) error { r.Header.Set("X-Key", "secret"); return nil }
	for _, path := range []string{"/away", "/home"} {
		req, _ := http.NewRequest(http.MethodGet, "https://a.example.com"+path, http.NoBody)
		resp, err := f.do(context.Background(), req, sign)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if got["a.example.com/away"] != "secret" || got["b.example.com/landed"] != "" {
		t.Errorf("cross-host redirect: %v", got)
	}
	if got["a.example.com/landed"] != "secret" {
		t.Errorf("same-host redirect lost the key: %v", got)
	}
}
