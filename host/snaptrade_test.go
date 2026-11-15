package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// SnapTrade's signature, against vectors from its documented JavaScript
// implementation (docs.snaptrade.com/docs/request-signatures).
func TestSnapSignatureMatchesSnapTradeJS(t *testing.T) {
	for _, c := range []struct {
		key, path, query string
		content          any
		want             string
	}{
		{"YOUR_CONSUMER_KEY", "/symbols", "clientId=YOUR_CLIENT_ID&timestamp=1700000000",
			map[string]any{"substring": "AAPL", "b": map[string]any{"z": 1, "a": []any{2, "<&>"}}},
			"mcda8Vy0e/+7Gw0wM3yELB1ilfrAhIUPY55ZNq9vK6k="},
		{"K", "/accounts", "clientId=C&timestamp=1&userId=u&userSecret=s", nil,
			"6PnGKIT4q8ShEcuKvrOU2AS457WNXGKPU2VQ9tXGhko="},
	} {
		got, err := snapSignature(c.key, c.content, c.path, c.query)
		if err != nil || got != c.want {
			t.Errorf("%s?%s: %q %v, want %q", c.path, c.query, got, err, c.want)
		}
	}
}

// fakeSnapTrade is SnapTrade's API: it checks every request's signature,
// and plays the Connection Portal's outcome with connType.
type fakeSnapTrade struct {
	mu        sync.Mutex
	users     map[string]string           // id → secret
	conns     map[string][]map[string]any // id → authorizations
	deleted   []string
	redirects map[string]string // id → customRedirect
	connType  atomic.Value      // the type the next connection gets
	trades    atomic.Bool       // can_access_trades
	reject    atomic.Bool       // refuse the widget's calls
	seen      []string          // "METHOD path", for widget calls
}

func (f *fakeSnapTrade) handler(t *testing.T) http.Handler {
	t.Helper()
	f.users, f.conns, f.redirects = map[string]string{}, map[string][]map[string]any{}, map[string]string{}
	f.connType.Store("read")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var content any
		if len(b) > 0 {
			if err := json.Unmarshal(b, &content); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
		}
		want, _ := snapSignature("ck", content, r.URL.EscapedPath(), r.URL.RawQuery)
		q := r.URL.Query()
		if r.Header.Get("Signature") != want || q.Get("clientId") != "cid" || q.Get("timestamp") == "" {
			t.Errorf("%s %s: bad signature or client", r.Method, r.URL.Path)
			http.Error(w, `{"detail":"bad signature"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		user := q.Get("userId")
		authed := user != "" && f.users[user] != "" && f.users[user] == q.Get("userSecret")
		body, _ := content.(map[string]any)
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.Method + " " + r.URL.Path {
		case "GET /snapTrade/partners":
			reply(map[string]any{"can_access_trades": f.trades.Load(), "can_access_holdings": true})
		case "POST /snapTrade/registerUser":
			id, _ := body["userId"].(string)
			f.users[id] = "sec-" + id
			reply(map[string]any{"userId": id, "userSecret": "sec-" + id})
		case "POST /snapTrade/login":
			if !authed || body["connectionType"] != "read" {
				http.Error(w, `{"detail":"no"}`, http.StatusBadRequest)
				return
			}
			f.redirects[user], _ = body["customRedirect"].(string)
			reply(map[string]any{"redirectURI": "https://app.snaptrade.com/portal?u=" + url.QueryEscape(user)})
		case "DELETE /snapTrade/deleteUser":
			f.deleted = append(f.deleted, user)
			delete(f.users, user)
			reply(map[string]any{"status": "deleted"})
		case "GET /authorizations":
			if !authed {
				http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			reply(append([]map[string]any{}, f.conns[user]...))
		case "GET /accounts":
			f.seen = append(f.seen, "GET /accounts?"+r.URL.RawQuery)
			if !authed || f.reject.Load() {
				http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			reply([]any{})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
}

// connect plays the person connecting a brokerage in the portal opened for
// the user in portal (a Connection Portal URL), and returns where the
// portal sends the phone back.
func (f *fakeSnapTrade) connect(portal string) string {
	u, _ := url.Parse(portal)
	user := u.Query().Get("u")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns[user] = append(f.conns[user], map[string]any{
		"id": "conn-1", "type": f.connType.Load(), "disabled": false, "brokerage": map[string]any{"name": "Robinhood"},
	})
	return f.redirects[user] + "?status=SUCCESS&connection_id=conn-1"
}

func (f *fakeSnapTrade) deletedUsers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
}

const snapHost = "snap.example.com"

type snapHarness struct {
	*harness
	snap    *fakeSnapTrade
	results chan string
}

// newSnapHarness: widget "ws" uses the built-in snaptrade provider [read];
// its run fetches /accounts, with credentials of its own in the query, and
// reports the status and whether the response showed it the user secret.
func newSnapHarness(t *testing.T) *snapHarness {
	t.Helper()
	snap := &fakeSnapTrade{}
	tr := publicTLS(t, snap.handler(t), true)
	results := make(chan string, 16)
	spec := widget.Spec{
		Name: "ws", Version: "1",
		Files:  []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Config: &testwidgetv1.Config{},
		View:   testSpec(nil).View,
		Run: func(ctx context.Context, h widget.Host) error {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+snapHost+"/accounts?userId=forged&userSecret=forged", http.NoBody)
			resp, err := h.Fetch(widget.Authorize(req))
			if err != nil {
				results <- "err: " + err.Error()
			} else {
				resp.Body.Close()
				results <- fmt.Sprintf("%s leaked=%v", resp.Status, strings.Contains(resp.Request.URL.String(), "sec-"))
			}
			<-ctx.Done()
			return ctx.Err()
		},
		Network: widget.NetworkRequest{Hosts: []string{snapHost}},
		Uses:    []widget.Use{{Provider: "snaptrade", Scopes: []string{"read"}}},
	}
	// The shipped manifest, on a host the test certificate covers.
	b, err := builtinProviderFS.ReadFile("providers/snaptrade.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := parseProvider(bytes.ReplaceAll(b, []byte("api.snaptrade.com"), []byte(snapHost)))
	if err != nil {
		t.Fatal(err)
	}
	providers := map[string]*provider{p.Name: p}
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "https://widgets.example.com", Widgets: []widget.Spec{spec},
		Instances: []Instance{{ID: "is", Widget: "ws"}}, transport: tr, providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	t.Cleanup(func() { h.http.Close(); srv.Close(context.Background()) })
	srv.Start()
	return &snapHarness{harness: h, snap: snap, results: results}
}

func (h *snapHarness) setKeys() {
	h.t.Helper()
	if _, err := h.adminClient().SetProviderClient(context.Background(), withCookie(&widgetdv1.SetProviderClientRequest{
		Provider: "snaptrade", ClientId: "cid", ClientSecret: "ck"}, h.adminCookie())); err != nil {
		h.t.Fatal(err)
	}
}

// signIn runs a new account's sign-in from the admin page, as the phone
// would: the QR link, the Connection Portal, the redirect back. It returns
// the final reply.
func (h *snapHarness) signIn() (*http.Response, string) {
	h.t.Helper()
	start, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "snaptrade", InstanceId: "is"}, h.adminCookie()))
	if err != nil {
		h.t.Fatal(err)
	}
	link, _ := url.Parse(start.Msg.GetUrl())
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(h.http.URL + link.Path)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	portal := resp.Header.Get("Location")
	if !strings.HasPrefix(portal, "https://app.snaptrade.com/portal") {
		h.t.Fatalf("the link should go to the Connection Portal: %d %q", resp.StatusCode, portal)
	}
	back, _ := url.Parse(h.snap.connect(portal))
	if back.Host != "widgets.example.com" {
		h.t.Fatalf("the portal should send the phone back here: %s", back)
	}
	resp, err = noFollow.Get(h.http.URL + back.Path + "?" + back.RawQuery)
	if err != nil {
		h.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (h *snapHarness) state() widgetdv1.InstanceStatus_State {
	h.srv.mu.Lock()
	in := h.srv.instances["is"]
	h.srv.mu.Unlock()
	return in.status().GetState()
}

func (h *snapHarness) waitState(want widgetdv1.InstanceStatus_State) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.state() != want {
		if time.Now().After(deadline) {
			h.t.Fatalf("state %v, want %v", h.state(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *snapHarness) waitDeleted(user string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(h.snap.deletedUsers(), user) {
		if time.Now().After(deadline) {
			h.t.Fatalf("SnapTrade user %s was never deleted (deleted: %v)", user, h.snap.deletedUsers())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSnapTradeAccount(t *testing.T) {
	h := newSnapHarness(t)
	h.waitState(NS)
	h.setKeys()

	resp, body := h.signIn()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	id := loc.Query().Get("name")
	if resp.StatusCode != http.StatusSeeOther || id == "" {
		t.Fatalf("a new account should be sent to be named: %d %s %s", resp.StatusCode, loc, body)
	}
	a, err := h.srv.loadAcct(id)
	if err != nil || a == nil || a.Token == nil || a.Token.User == "" || !slices.Equal(a.Token.Granted, []string{"read"}) {
		t.Fatalf("account: %+v %v", a, err)
	}
	if _, err := h.adminClient().LabelAccount(context.Background(), withCookie(&widgetdv1.LabelAccountRequest{AccountId: id, Label: "Robinhood"}, h.adminCookie())); err != nil {
		t.Fatal(err)
	}
	h.waitState(RUN)

	// The widget's fetch is signed as the account's user, its own
	// credentials replaced, and the response doesn't show it the secret.
	select {
	case r := <-h.results:
		if r != "200 OK leaked=false" {
			t.Fatalf("widget fetch: %s", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the widget never fetched")
	}
	h.snap.mu.Lock()
	seen := slices.Clone(h.snap.seen)
	h.snap.mu.Unlock()
	if len(seen) != 1 || strings.Contains(seen[0], "forged") || !strings.Contains(seen[0], "userId="+a.Token.User) {
		t.Fatalf("SnapTrade saw %v", seen)
	}

	// read covers GETs of the user's own data, nothing else.
	in := h.srv.instances["is"]
	for _, c := range []struct{ method, raw string }{
		{"POST", "https://" + snapHost + "/trade/place"},
		{"POST", "https://" + snapHost + "/accounts/x/trading/simple"},
		{"DELETE", "https://" + snapHost + "/authorizations/conn-1"},
		{"GET", "https://" + snapHost + "/snapTrade/listUsers"},
		{"GET", "https://" + snapHost + "/snapTrade/partners"},
	} {
		if _, _, _, err := h.srv.authorization(context.Background(), in, c.method, mustURL(t, c.raw)); err == nil {
			t.Errorf("signed %s %s", c.method, c.raw)
		}
	}

	// Signing out deletes the SnapTrade user, connections and all.
	if _, err := h.adminClient().SignOut(context.Background(), withCookie(&widgetdv1.SignOutRequest{AccountId: id}, h.adminCookie())); err != nil {
		t.Fatal(err)
	}
	h.waitDeleted(a.Token.User)
	h.waitState(NS)
}

func TestSnapTradeRefusesTradingConnection(t *testing.T) {
	h := newSnapHarness(t)
	h.setKeys()
	h.snap.connType.Store("trade")
	resp, body := h.signIn()
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "allows trading") {
		t.Fatalf("a trading connection should be refused: %d %s", resp.StatusCode, body)
	}
	accts, err := h.srv.listAccts("snaptrade")
	if err != nil || len(accts) != 0 {
		t.Fatalf("no account should be made: %v %v", accts, err)
	}
	h.snap.mu.Lock()
	var users []string
	for u := range h.snap.conns {
		users = append(users, u)
	}
	h.snap.mu.Unlock()
	if len(users) != 1 {
		t.Fatalf("users: %v", users)
	}
	h.waitDeleted(users[0])
}

// A refused call signs the account out, and deletes its SnapTrade user
// rather than leave a live connection nothing here can reach.
func TestSnapTradeLapseDeletesUser(t *testing.T) {
	h := newSnapHarness(t)
	h.setKeys()
	resp, _ := h.signIn()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	id := loc.Query().Get("name")
	if _, err := h.adminClient().LabelAccount(context.Background(), withCookie(&widgetdv1.LabelAccountRequest{AccountId: id, Label: "Robinhood"}, h.adminCookie())); err != nil {
		t.Fatal(err)
	}
	h.waitState(RUN)
	<-h.results
	a, _ := h.srv.loadAcct(id)
	h.snap.reject.Store(true)
	h.srv.mu.Lock()
	h.srv.stopLocked(h.srv.instances["is"])
	h.srv.startIfReadyLocked(h.srv.instances["is"])
	h.srv.mu.Unlock()
	h.waitState(NS)
	h.waitDeleted(a.Token.User)
}

func TestSnapTradeKeysChecked(t *testing.T) {
	h := newSnapHarness(t)
	h.snap.trades.Store(true)
	h.setKeys()
	if e := h.srv.accountError("snaptrade"); !strings.Contains(e, "Trading is enabled") {
		t.Fatalf("keys allowing trading should be flagged: %q", e)
	}
	h.snap.trades.Store(false)
	h.setKeys()
	if e := h.srv.accountError("snaptrade"); e != "" {
		t.Fatalf("good keys: %q", e)
	}
}

// A request body is signed as SnapTrade canonicalizes it.
func TestSnapSignBody(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "https://"+snapHost+"/x?a=1", bytes.NewReader([]byte(`{"b":1,"a":"<x>"}`)))
	if err := snapSign(r, signInClient{id: "cid", secret: "ck"}, &token{Access: "s", User: "u"}); err != nil {
		t.Fatal(err)
	}
	want, _ := snapSignature("ck", map[string]any{"a": "<x>", "b": 1}, "/x", r.URL.RawQuery)
	if r.Header.Get("Signature") != want {
		t.Fatalf("signature %q, want %q", r.Header.Get("Signature"), want)
	}
	if b, _ := io.ReadAll(r.Body); string(b) != `{"b":1,"a":"<x>"}` {
		t.Fatalf("the body should be sent as the widget wrote it: %s", b)
	}
}
