package host

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// fakeProvider is an OAuth device-flow provider and the API it guards, on
// one TLS server that every host name resolves to.
type fakeProvider struct {
	polls    atomic.Int32
	approved atomic.Bool
	revoked  atomic.Bool
}

func (p *fakeProvider) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "app-1" || r.Form.Get("scope") != "read" {
			io.WriteString(w, `{"error":"bad_client"}`)
			return
		}
		io.WriteString(w, `{"device_code":"dev-1","user_code":"WDGT-1234","verification_uri":"https://auth.example.com/activate","expires_in":60,"interval":1}`)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p.polls.Add(1)
		if r.Form.Get("device_code") != "dev-1" {
			io.WriteString(w, `{"error":"bad_verification_code"}`)
			return
		}
		if !p.approved.Load() {
			io.WriteString(w, `{"error":"authorization_pending"}`)
			return
		}
		io.WriteString(w, `{"access_token":"tok-123","token_type":"bearer"}`)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		if p.revoked.Load() || r.Header.Get("Authorization") != "Bearer tok-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "me")
	})
	return mux
}

func TestSignIn(t *testing.T) {
	p := &fakeProvider{}
	_, tr := publicTLS(t, p.handler(t), true)
	results := make(chan string, 8)
	spec := testSpec(func(ctx context.Context, h widget.Host) error {
		for {
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.example.com/me", nil)
			resp, err := h.Fetch(widget.Authorize(req))
			if err != nil {
				results <- "error: " + err.Error()
			} else {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				results <- resp.Status + " " + string(b)
			}
			// The token must never go anywhere else.
			other, _ := http.NewRequestWithContext(ctx, "GET", "https://b.example.com/x", nil)
			if _, err := h.Fetch(widget.Authorize(other)); err == nil {
				results <- "LEAK: signed a request to another host"
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	})
	spec.Network = widget.NetworkRequest{Hosts: []string{"api.example.com", "b.example.com"}}
	spec.SignIn = &widget.SignIn{
		Provider: "Test", DeviceURL: "https://auth.example.com/device", TokenURL: "https://auth.example.com/token",
		Scopes: []string{"read"}, APIHosts: []string{"api.example.com"},
	}
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec},
		Instances: []Instance{{ID: "one", Widget: "test"}}, transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	srv.Start()
	t.Cleanup(func() { h.http.Close(); srv.Close(context.Background()) })
	c, admin, device, ctx := h.adminClient(), h.adminCookie(), h.deviceCookie("d1"), context.Background()
	f := h.open(device)

	if st := f.nextInstance("one"); st.GetState() != widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN {
		t.Fatalf("an instance with no account should wait to be signed in: %v", st)
	}
	if _, err := c.BeginSignIn(ctx, withCookie(&widgetdv1.BeginSignInRequest{InstanceId: "one"}, device)); err == nil || !strings.Contains(err.Error(), "client ID") {
		t.Fatalf("with no app registered: %v", err)
	}
	if _, err := c.SetSignInClient(ctx, withCookie(&widgetdv1.SetSignInClientRequest{Widget: "test", ClientId: "app-1"}, device)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a screen registering an app: %v", err)
	}
	if _, err := c.SetSignInClient(ctx, withCookie(&widgetdv1.SetSignInClientRequest{Widget: "test", ClientId: "app-1"}, admin)); err != nil {
		t.Fatal(err)
	}

	// The screen starts it: there's no account to replace.
	code, err := c.BeginSignIn(ctx, withCookie(&widgetdv1.BeginSignInRequest{InstanceId: "one"}, device))
	if err != nil {
		t.Fatal(err)
	}
	if code.Msg.GetUserCode() != "WDGT-1234" || code.Msg.GetProvider() != "Test" {
		t.Fatalf("code = %v", code.Msg)
	}
	// Pending until approved...
	deadline := time.Now().Add(10 * time.Second)
	for p.polls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	p.approved.Store(true)
	// ...then running, with signed requests that work.
	for {
		st := f.nextInstance("one")
		if st.GetState() == widgetdv1.InstanceStatus_STATE_RUNNING {
			break
		}
	}
	if got := <-results; got != "200 OK me" {
		t.Fatalf("signed request: %q", got)
	}
	var sealed []byte
	srv.db.QueryRow(`SELECT value FROM tokens WHERE instance = 'one'`).Scan(&sealed)
	if len(sealed) == 0 || bytes.Contains(sealed, []byte("tok-123")) {
		t.Fatal("the token should be stored, and only sealed")
	}
	if _, err := c.BeginSignIn(ctx, withCookie(&widgetdv1.BeginSignInRequest{InstanceId: "one"}, device)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a screen replacing a sign-in: %v", err)
	}

	// The provider revokes it: the instance stops and asks again.
	p.revoked.Store(true)
	for {
		st := f.nextInstance("one")
		if st.GetState() == widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN {
			if !strings.Contains(st.GetMessage(), "lapsed") {
				t.Fatalf("message = %q", st.GetMessage())
			}
			break
		}
	}
	if tok, _ := srv.loadToken("one"); tok != nil {
		t.Fatal("a lapsed token should be forgotten")
	}
	for len(results) > 0 {
		if r := <-results; strings.HasPrefix(r, "LEAK") {
			t.Fatal(r)
		}
	}
}

func TestSignInManifestChecks(t *testing.T) {
	base := testSpec(func(context.Context, widget.Host) error { return nil })
	si := func(mod func(*widget.SignIn)) widget.Spec {
		s := base
		v := &widget.SignIn{Provider: "P", DeviceURL: "https://a.example.com/d", TokenURL: "https://a.example.com/t", APIHosts: []string{"a.example.com"}}
		mod(v)
		s.SignIn = v
		return s
	}
	for name, spec := range map[string]widget.Spec{
		"plain http endpoint":     si(func(v *widget.SignIn) { v.TokenURL = "http://a.example.com/t" }),
		"api host not in network": si(func(v *widget.SignIn) { v.APIHosts = []string{"evil.example.com"} }),
		"no api hosts":            si(func(v *widget.SignIn) { v.APIHosts = nil }),
		"no provider name":        si(func(v *widget.SignIn) { v.Provider = "" }),
	} {
		if _, err := loadWidget(spec); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	if _, err := loadWidget(si(func(*widget.SignIn) {})); err != nil {
		t.Fatalf("a good sign-in: %v", err)
	}
}

func TestRefreshUsesClientSecret(t *testing.T) {
	var gotSecret atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotSecret.Store(r.Form.Get("client_secret"))
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "ref-1" || r.Form.Get("client_secret") != "s3cret" {
			io.WriteString(w, `{"error":"bad_refresh_token"}`)
			return
		}
		io.WriteString(w, `{"access_token":"tok-new","token_type":"bearer","expires_in":28800,"refresh_token":"ref-2"}`)
	})
	_, tr := publicTLS(t, mux, true)
	spec := testSpec(func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() })
	spec.SignIn = &widget.SignIn{Provider: "Test", DeviceURL: "https://auth.example.com/device", TokenURL: "https://auth.example.com/token", APIHosts: []string{"a.example.com"}}
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec},
		Instances: []Instance{{ID: "one", Widget: "test"}}, transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close(context.Background()) })

	// ID and secret separately: each save keeps the other.
	if err := srv.setClient("test", "app-1", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := srv.setClient("test", "app-1", ""); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := srv.loadClient("test"); c.secret != "s3cret" {
		t.Fatalf("saving without a secret dropped it: %+v", c)
	}

	// A token about to expire is renewed, with the secret, before use.
	srv.storeToken("one", &token{Access: "tok-old", Refresh: "ref-1", Expiry: time.Now().Add(30 * time.Second)})
	u, _ := url.Parse("https://a.example.com/me")
	auth, err := srv.authorization(context.Background(), srv.instances["one"], u)
	if err != nil || auth != "Bearer tok-new" {
		t.Fatalf("authorization = %q, %v (secret sent: %v)", auth, err, gotSecret.Load())
	}
	if tok, _ := srv.loadToken("one"); tok.Refresh != "ref-2" || time.Until(tok.Expiry) < time.Hour {
		t.Fatalf("stored after refresh: %+v", tok)
	}
}
