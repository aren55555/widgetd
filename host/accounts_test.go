package host

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

	abiv1 "github.com/aren55555/widgetd/gen/widgetd/abi/v1"
	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// fakeIdP is an OAuth provider (browser and device flows) and the API it
// guards, on one TLS server every host name resolves to.
type fakeIdP struct {
	mu        sync.Mutex
	revoked   []string // tokens revoked, in order
	codes     map[string]string // code → PKCE challenge it was issued for
	scopeFor  map[string]string // code → scopes it grants
	approved  atomic.Bool
	reject    atomic.Bool
	tokenSeq  atomic.Int32
	lastToken atomic.Value
	who       atomic.Value      // the person consenting now
	whoFor    map[string]string // code or token → person
}

func (f *fakeIdP) person() string {
	if w, ok := f.who.Load().(string); ok {
		return w
	}
	return "me@work"
}

func (f *fakeIdP) handler() http.Handler {
	f.codes, f.scopeFor, f.whoFor = map[string]string{}, map[string]string{}, map[string]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mint := func(scope, who string) {
			tok := "tok-" + string(rune('a'+f.tokenSeq.Add(1)))
			f.lastToken.Store(tok)
			f.mu.Lock()
			f.whoFor[tok] = who
			f.mu.Unlock()
			io.WriteString(w, `{"access_token":"`+tok+`","token_type":"bearer","refresh_token":"ref-`+tok+`","expires_in":3600,"scope":"`+scope+`"}`)
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			f.mu.Lock()
			challenge, ok := f.codes[r.Form.Get("code")]
			scope := f.scopeFor[r.Form.Get("code")]
			who := f.whoFor[r.Form.Get("code")]
			f.mu.Unlock()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.Form.Get("client_secret") != "sec" {
				io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			mint(scope, who)
		case "urn:ietf:params:oauth:grant-type:device_code":
			if !f.approved.Load() {
				io.WriteString(w, `{"error":"authorization_pending"}`)
				return
			}
			mint("a,b", f.person())
		}
	})
	mux.HandleFunc("POST /device", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"device_code":"d","user_code":"WDGT-1","verification_uri":"https://idp.example.com/activate","expires_in":60,"interval":1}`)
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.revoked = append(f.revoked, r.Form.Get("token"))
		f.mu.Unlock()
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		who := f.whoFor[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		f.mu.Unlock()
		io.WriteString(w, `{"email":"`+who+`"}`)
	})
	mux.HandleFunc("GET /b/me", func(w http.ResponseWriter, r *http.Request) {
		if f.reject.Load() || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "b")
	})
	mux.HandleFunc("GET /needs-c", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="c"`)
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		if f.reject.Load() || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "me")
	})
	return mux
}

// issue plays the provider's consent page: it reads the consent URL the
// host sent the phone to and returns where the provider redirects back.
func (f *fakeIdP) issue(t *testing.T, consent *url.URL) string {
	t.Helper()
	q := consent.Query()
	code := "code-" + q.Get("state")
	f.mu.Lock()
	f.codes[code], f.scopeFor[code], f.whoFor[code] = q.Get("code_challenge"), q.Get("scope"), f.person()
	f.mu.Unlock()
	return q.Get("redirect_uri") + "?state=" + url.QueryEscape(q.Get("state")) + "&code=" + code
}

func (f *fakeIdP) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.revoked)
}

const fakeIdPManifest = `
name = "idp"
title = "IdP"
[sign_in]
flow = "browser"
auth_url = "https://idp.example.com/auth"
token_url = "https://idp.example.com/token"
revoke = "rfc7009"
revoke_url = "https://idp.example.com/revoke"
[sign_in.auth_params]
access_type = "offline"
[identity]
url = "https://idp.example.com/userinfo"
field = "email"
scopes = ["openid"]
[scopes]
rejected_pattern = 'invalid=\[([^\]]*)\]'
[scopes.covers]
a = ["GET https://api.example.com/me", "GET https://api.example.com/needs-c"]
b = ["GET https://api.example.com/b/"]
c = ["GET https://api.example.com/c/"]
`

const fakeDevManifest = `
name = "dev"
title = "Dev"
[sign_in]
flow = "device"
device_url = "https://idp.example.com/device"
token_url = "https://idp.example.com/token"
revoke = "rfc7009"
revoke_url = "https://idp.example.com/revoke"
[scopes.covers]
a = ["https://devapi.example.com/"]
b = ["https://devapi.example.com/"]
`

func fakeProviders(t *testing.T) map[string]*provider {
	t.Helper()
	out := map[string]*provider{}
	for _, m := range []string{fakeIdPManifest, fakeDevManifest} {
		p, err := parseProvider([]byte(m))
		if err != nil {
			t.Fatal(err)
		}
		out[p.Name] = p
	}
	return out
}

// accountHarness: widget "wa" uses idp [a], widget "wb" uses idp [b]; one
// instance of each. Their runs report signed fetches on results.
type accountHarness struct {
	*harness
	idp     *fakeIdP
	results chan string
}

func newAccountHarness(t *testing.T, extra ...widget.Spec) *accountHarness {
	t.Helper()
	idp := &fakeIdP{}
	_, tr := publicTLS(t, idp.handler(), true)
	results := make(chan string, 64)
	mk := func(name, scope, path string) widget.Spec {
		return widget.Spec{
			Name: name, Version: "1",
			Files:  []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
			Config: &testwidgetv1.Config{},
			View:   testSpec(nil).View,
			Run: func(ctx context.Context, h widget.Host) error {
				req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.example.com"+path, nil)
				resp, err := h.Fetch(widget.Authorize(req))
				if err != nil {
					results <- name + ": " + err.Error()
				} else {
					resp.Body.Close()
					results <- name + ": " + resp.Status
				}
				<-ctx.Done()
				return ctx.Err()
			},
			Network: widget.NetworkRequest{Hosts: []string{"api.example.com", "devapi.example.com"}},
			Uses:    []widget.Use{{Provider: "idp", Scopes: []string{scope}}},
		}
	}
	specs := append([]widget.Spec{mk("wa", "a", "/me"), mk("wb", "b", "/b/me")}, extra...)
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "https://widgets.example.com", Widgets: specs,
		Instances: []Instance{{ID: "ia", Widget: "wa"}, {ID: "ib", Widget: "wb"}},
		transport: tr, providers: fakeProviders(t)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	t.Cleanup(func() { h.http.Close(); srv.Close(context.Background()) })
	if err := srv.setClient("idp", "client-1", "sec"); err != nil {
		t.Fatal(err)
	}
	if err := srv.setClient("dev", "client-2", ""); err != nil {
		t.Fatal(err)
	}
	srv.Start()
	return &accountHarness{harness: h, idp: idp, results: results}
}

func (h *accountHarness) state(id string) widgetdv1.InstanceStatus_State {
	h.srv.mu.Lock()
	in := h.srv.instances[id]
	h.srv.mu.Unlock()
	return in.status().GetState()
}

func (h *accountHarness) waitState(id string, want widgetdv1.InstanceStatus_State) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.state(id) != want {
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: state %v, want %v", id, h.state(id), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// browse runs the browser flow as a phone would, as person: the QR link,
// the provider's consent page, the redirect back. It returns the callback's
// reply.
func (h *accountHarness) browse(start *widgetdv1.BeginSignInResponse, person string) (*http.Response, string) {
	h.t.Helper()
	h.idp.who.Store(person)
	link, _ := url.Parse(start.GetUrl()) // on the public URL; follow it here
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(h.http.URL + link.Path)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	consent, _ := url.Parse(resp.Header.Get("Location"))
	back, _ := url.Parse(h.idp.issue(h.t, consent))
	resp, err = noFollow.Get(h.http.URL + back.Path + "?" + back.RawQuery)
	if err != nil {
		h.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// newAccount signs a new idp account in from the admin page, for an
// instance, as person, and names it label ("" leaves it unnamed).
func (h *accountHarness) newAccount(instance, person, label string) string {
	h.t.Helper()
	start, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "idp", InstanceId: instance}, h.adminCookie()))
	if err != nil {
		h.t.Fatal(err)
	}
	resp, body := h.browse(start.Msg, person)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	id := loc.Query().Get("name")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasSuffix(loc.Path, "/admin") || id == "" {
		h.t.Fatalf("a new account's callback should send the phone to name it: %d %s %s", resp.StatusCode, loc, body)
	}
	if label != "" {
		h.label(id, label)
	}
	return id
}

func (h *accountHarness) label(id, label string) {
	h.t.Helper()
	if _, err := h.adminClient().LabelAccount(context.Background(), withCookie(&widgetdv1.LabelAccountRequest{AccountId: id, Label: label}, h.adminCookie())); err != nil {
		h.t.Fatal(err)
	}
}

func (h *accountHarness) bind(instance, acct string) {
	h.t.Helper()
	if _, err := h.adminClient().BindAccount(context.Background(), withCookie(&widgetdv1.BindAccountRequest{InstanceId: instance, Provider: "idp", AccountId: acct}, h.adminCookie())); err != nil {
		h.t.Fatal(err)
	}
}

// signInAgain signs an existing account in again, as person, from a
// screen (or the admin page, with the admin cookie).
func (h *accountHarness) signInAgain(acct, person string, cookie string) (int, string) {
	h.t.Helper()
	start, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "idp", AccountId: acct}, cookie))
	if err != nil {
		h.t.Fatal(err)
	}
	resp, body := h.browse(start.Msg, person)
	return resp.StatusCode, body
}

func (h *accountHarness) acct(id string) *account {
	h.t.Helper()
	a, err := h.srv.loadAcct(id)
	if err != nil || a == nil {
		h.t.Fatalf("account %s: %v %v", id, a, err)
	}
	return a
}

func (h *accountHarness) screen() string {
	return h.deviceCookie("screen-" + time.Now().Format("150405.000000000"))
}

// workForBoth signs "Work" in covering both ia and ib.
func (h *accountHarness) workForBoth() string {
	h.t.Helper()
	work := h.newAccount("ia", "me@work", "Work")
	h.bind("ib", work)
	if code, body := h.signInAgain(work, "me@work", h.adminCookie()); code != 200 {
		h.t.Fatalf("signing Work in again: %d %s", code, body)
	}
	return work
}

func (h *accountHarness) waitRevoked(tok string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(h.idp.revokedTokens(), tok) {
		if time.Now().After(deadline) {
			h.t.Fatalf("%s was never revoked with the provider", tok)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const (
	NS  = widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN
	RUN = widgetdv1.InstanceStatus_STATE_RUNNING
)

func TestAccountsFollowTheBoard(t *testing.T) {
	h := newAccountHarness(t)

	// No account bound: both wait, for an admin to choose one.
	h.waitState("ia", NS)
	h.waitState("ib", NS)
	if st := h.srv.instances["ia"].status(); st.GetProvider() != "idp" || st.GetAccount() != "" {
		t.Fatalf("waiting for %q/%q, want idp and no account", st.GetProvider(), st.GetAccount())
	}

	// A new account for ia; then ib bound to it too, which needs b as well.
	work := h.newAccount("ia", "me@work", "Work")
	h.waitState("ia", RUN)
	h.bind("ib", work)
	if st := h.srv.instances["ib"].status(); st.GetState() != NS || st.GetAccount() != work || st.GetAccountLabel() != "Work" {
		t.Fatalf("ib = %v: want waiting for Work to cover it", st)
	}
	if h.state("ia") != RUN {
		t.Fatal("growing an account must not stop the widgets it covers")
	}
	h.signInAgain(work, "me@work", h.adminCookie())
	h.waitState("ib", RUN)
	if a := h.acct(work); !slices.Equal(a.Token.Granted, []string{"a", "b", "openid"}) || a.Subject != "me@work" {
		t.Fatalf("Work = %v as %q, want the union [a b] plus identity, as me@work", a.Token.Granted, a.Subject)
	}

	// Remove a widget: revoked at once; what's left waits for less.
	signedWith := h.acct(work).Token.Refresh // rfc7009 revokes the refresh token
	if err := h.srv.deleteInstance("ib"); err != nil {
		t.Fatal(err)
	}
	h.waitState("ia", NS)
	if h.acct(work).Token != nil {
		t.Fatal("a shrunk board must not keep the larger grant")
	}
	h.waitRevoked(signedWith)
	h.signInAgain(work, "me@work", h.screen())
	h.waitState("ia", RUN)
	if a := h.acct(work); !slices.Equal(a.Token.Granted, []string{"a", "openid"}) {
		t.Fatalf("after shrinking: granted %v, want [a openid]", a.Token.Granted)
	}

	// Remove the last: revoked, but the account stays, named.
	if err := h.srv.deleteInstance("ia"); err != nil {
		t.Fatal(err)
	}
	if a := h.acct(work); a.Token != nil || a.Label != "Work" {
		t.Fatalf("Work = %+v: want signed out, still named", a)
	}
}

func TestTwoAccountsOneProvider(t *testing.T) {
	h := newAccountHarness(t)
	work := h.newAccount("ia", "me@work", "Work")
	home := h.newAccount("ib", "me@home", "Personal")
	h.waitState("ia", RUN)
	h.waitState("ib", RUN)
	u := mustURL(t, "https://api.example.com/me")
	authA, _, acctA, err := h.srv.authorization(context.Background(), h.srv.instances["ia"], "GET", u)
	if err != nil || acctA != work || authA != "Bearer "+h.acct(work).Token.Access {
		t.Fatalf("ia signed with %q (%s), %v; want Work's token", authA, acctA, err)
	}
	_, _, acctB, _ := h.srv.authorization(context.Background(), h.srv.instances["ib"], "GET", mustURL(t, "https://api.example.com/b/me"))
	if acctB != home {
		t.Fatalf("ib signed with %s, want Personal", acctB)
	}
	// Removing ib touches only Personal.
	if err := h.srv.deleteInstance("ib"); err != nil {
		t.Fatal(err)
	}
	if h.acct(home).Token != nil || h.acct(work).Token == nil || h.state("ia") != RUN {
		t.Fatal("removing Personal's widget should revoke Personal only")
	}
}

func TestNewAccountMustBeNamed(t *testing.T) {
	h := newAccountHarness(t)
	id := h.newAccount("ia", "me@work", "")
	h.waitState("ia", NS)
	if msg := h.srv.instances["ia"].status().GetMessage(); !strings.Contains(msg, "named") {
		t.Fatalf("message %q should say it's waiting for a name", msg)
	}
	if _, _, _, err := h.srv.authorization(context.Background(), h.srv.instances["ia"], "GET", mustURL(t, "https://api.example.com/me")); err == nil {
		t.Fatal("an unnamed account must not sign anything")
	}
	h.label(id, "Work")
	h.waitState("ia", RUN)

	// Names are unique per provider.
	other := h.newAccount("ib", "me@home", "")
	_, err := h.adminClient().LabelAccount(context.Background(), withCookie(&widgetdv1.LabelAccountRequest{AccountId: other, Label: "work"}, h.adminCookie()))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("a duplicate name: %v", err)
	}
	// Only an admin adds an account.
	_, err = h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "idp", InstanceId: "ib"}, h.screen()))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a screen adding an account: %v", err)
	}
}

func TestSigningInAsSomeoneElseIsRefused(t *testing.T) {
	h := newAccountHarness(t)
	work := h.newAccount("ia", "me@work", "Work")
	h.waitState("ia", RUN)
	h.srv.accountLapsed(work) // as if Google stopped accepting it
	h.waitState("ia", NS)
	code, body := h.signInAgain(work, "me@home", h.screen())
	if code != http.StatusConflict || !strings.Contains(body, "Sign in as me@work") {
		t.Fatalf("signing Work in as me@home: %d %s", code, body)
	}
	if h.acct(work).Token != nil || h.state("ia") != NS {
		t.Fatal("the wrong person's token must not be kept")
	}
	h.waitRevoked("ref-" + h.idp.lastToken.Load().(string))
}

func TestSamePersonTwiceIsRefused(t *testing.T) {
	h := newAccountHarness(t)
	work := h.newAccount("ia", "me@work", "Work")
	h.waitState("ia", RUN)
	start, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "idp", InstanceId: "ib"}, h.adminCookie()))
	if err != nil {
		t.Fatal(err)
	}
	resp, body := h.browse(start.Msg, "me@work")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "already the account") {
		t.Fatalf("me@work twice: %d %s", resp.StatusCode, body)
	}
	if accts, _ := h.srv.listAccts("idp"); len(accts) != 1 {
		t.Fatalf("%d accounts, want only Work", len(accts))
	}
	// Revoking the duplicate ends the person's grant, Work's included.
	if h.acct(work).Token != nil {
		t.Fatal("Work shares the revoked grant, so it must be signed out too")
	}
}

func TestScreensSignBackInOnly(t *testing.T) {
	h := newAccountHarness(t)
	work := h.workForBoth()
	h.waitState("ib", RUN)
	// Covered already: a screen can't replace it.
	_, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "idp", AccountId: work}, h.screen()))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a screen replacing a sufficient sign-in: %v", err)
	}
	// Unbinding sends the instance back to waiting for a choice.
	h.bind("ib", "")
	h.waitState("ib", NS)
	if h.srv.instances["ib"].status().GetAccount() != "" {
		t.Fatal("an unbound instance waits for no particular account")
	}
	// Deleting the account unbinds what used it.
	if _, err := h.adminClient().DeleteAccount(context.Background(), withCookie(&widgetdv1.DeleteAccountRequest{AccountId: work}, h.adminCookie())); err != nil {
		t.Fatal(err)
	}
	h.waitState("ia", NS)
	if a, _ := h.srv.loadAcct(work); a != nil || h.srv.instances["ia"].binding("idp") != "" {
		t.Fatal("a deleted account should be gone, and unbound")
	}
}

func TestSignedOnlyForOwnProvider(t *testing.T) {
	h := newAccountHarness(t)
	h.newAccount("ia", "me@work", "Work")
	in := h.srv.instances["ia"]
	for _, raw := range []string{"https://devapi.example.com/me", "https://evil.example.com/me", "http://api.example.com/me"} {
		if _, _, _, err := h.srv.authorization(context.Background(), in, "GET", mustURL(t, raw)); err == nil {
			t.Errorf("signed a request to %s", raw)
		}
	}
	if auth, p, _, err := h.srv.authorization(context.Background(), in, "GET", mustURL(t, "https://api.example.com/me")); err != nil || p != "idp" || !strings.HasPrefix(auth, "Bearer tok-") {
		t.Fatalf("own provider: %q %q %v", auth, p, err)
	}
}

func TestLapsedAccountWaitsAgain(t *testing.T) {
	h := newAccountHarness(t)
	work := h.workForBoth()
	h.waitState("ib", RUN)
	h.idp.reject.Store(true)
	// Restart wa so it fetches again and meets the 401.
	h.srv.mu.Lock()
	h.srv.stopLocked(h.srv.instances["ia"])
	h.srv.startIfReadyLocked(h.srv.instances["ia"])
	h.srv.mu.Unlock()
	h.waitState("ib", NS)
	if a := h.acct(work); a.Token != nil || a.Label != "Work" {
		t.Fatal("a token the provider rejected should be forgotten, the account kept")
	}
}

func TestDeviceFlowAccount(t *testing.T) {
	devSpec := widget.Spec{
		Name: "wd", Version: "1",
		Files:   []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Config:  &testwidgetv1.Config{},
		View:    testSpec(nil).View,
		Run:     func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() },
		Network: widget.NetworkRequest{Hosts: []string{"devapi.example.com"}},
		Uses:    []widget.Use{{Provider: "dev", Scopes: []string{"a", "b"}}},
	}
	h := newAccountHarness(t, devSpec)
	in, err := h.srv.addInstance("wd")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.srv.configure(in.id, "", nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(in.id, NS)
	start, err := h.adminClient().BeginSignIn(context.Background(), withCookie(&widgetdv1.BeginSignInRequest{Provider: "dev", InstanceId: in.id}, h.adminCookie()))
	if err != nil || start.Msg.GetUserCode() != "WDGT-1" {
		t.Fatalf("device start: %v %v", start, err)
	}
	h.idp.approved.Store(true)
	var id string
	deadline := time.Now().Add(10 * time.Second)
	for id == "" {
		if accts, _ := h.srv.listAccts("dev"); len(accts) == 1 {
			id = accts[0].ID
		} else if time.Now().After(deadline) {
			t.Fatal("the device sign-in never arrived")
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	list, err := h.adminClient().ListAccounts(context.Background(), withCookie(&widgetdv1.ListAccountsRequest{}, h.adminCookie()))
	if err != nil {
		t.Fatal(err)
	}
	var shown *widgetdv1.Account
	for _, a := range list.Msg.GetAccounts() {
		if a.GetId() == id {
			shown = a
		}
	}
	if shown == nil || shown.GetLabel() != "" || !strings.Contains(shown.GetMessage(), "Name") {
		t.Fatalf("the admin page should be asked to name it: %v", shown)
	}
	if _, err := h.adminClient().LabelAccount(context.Background(), withCookie(&widgetdv1.LabelAccountRequest{AccountId: id, Label: "Mine"}, h.adminCookie())); err != nil {
		t.Fatal(err)
	}
	h.waitState(in.id, RUN)
	if a := h.acct(id); !slices.Equal(a.Token.Granted, []string{"a", "b"}) {
		t.Fatalf("comma-separated scopes: granted %v", a.Token.Granted)
	}
}

// A sandboxed widget asks for signing with the Fetch call's authorize flag,
// not a header: the host must honour it the same way. (It once didn't, and
// a freshly signed-in sandboxed widget got 401s.)
func TestSandboxedFetchIsSigned(t *testing.T) {
	h := newAccountHarness(t)
	h.newAccount("ia", "me@work", "Work")
	in := h.srv.instances["ia"]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &guest{h: &runHost{in: in, ctx: ctx, cancel: cancel, cards: map[string]*card{}}, cards: map[string]*rawCard{}}
	res := g.fetch(ctx, &abiv1.Fetch{Method: "GET", Url: "https://api.example.com/me", Authorize: true})
	if res.GetError() != "" || res.GetFetch().GetStatus() != 200 {
		t.Fatalf("authorized sandboxed fetch: %v", res)
	}
	res = g.fetch(ctx, &abiv1.Fetch{Method: "GET", Url: "https://api.example.com/me", Headers: []*abiv1.Header{{Name: "Widgetd-Authorize", Value: "1"}}})
	if res.GetFetch().GetStatus() != 401 {
		t.Fatalf("unflagged fetch got %v; want it unsigned", res.GetFetch().GetStatus())
	}
}

func TestUsesUnknownProviderRefused(t *testing.T) {
	spec := testSpec(func(context.Context, widget.Host) error { return nil })
	spec.Uses = []widget.Use{{Provider: "nope", Scopes: []string{"x"}}}
	if _, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec}}); err == nil {
		t.Fatal("a widget using an unknown provider should be refused")
	}
}

func TestClientMigratesFromWidgetToProvider(t *testing.T) {
	dir := t.TempDir()
	spec := testSpec(func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() })
	spec.Uses = []widget.Use{{Provider: "github", Scopes: []string{"notifications"}}}
	spec.Network = widget.NetworkRequest{Hosts: []string{"api.github.com"}}
	srv, err := New(Options{StateDir: dir, PublicURL: "http://localhost", Widgets: []widget.Spec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	// As the old per-widget storage had it.
	sealed := srv.sealer.seal([]byte("old-secret"), "client:test", "secret")
	srv.db.Exec(`INSERT INTO sign_in_clients (widget, client_id, client_secret) VALUES ('test', 'Ov23old', ?)`, sealed)
	srv.Close(context.Background())

	srv, err = New(Options{StateDir: dir, PublicURL: "http://localhost", Widgets: []widget.Spec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())
	if c, ok, _ := srv.loadClient("github"); !ok || c.id != "Ov23old" || c.secret != "old-secret" {
		t.Fatalf("migrated client = %+v, %v", c, ok)
	}
}
