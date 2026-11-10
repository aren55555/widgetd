package host

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestBuiltinProviders(t *testing.T) {
	ps, err := loadBuiltinProviders()
	if err != nil {
		t.Fatal(err)
	}
	g, gh := ps["google"], ps["github"]
	if g == nil || gh == nil {
		t.Fatalf("built-ins: %v", ps)
	}
	// The GitHub widget's calls: /user on any token, /notifications by scope.
	for _, c := range []struct {
		p      *provider
		scopes []string
		method string
		url    string
		want   bool
	}{
		{gh, []string{"notifications"}, "GET", "https://api.github.com/user", true},
		{gh, []string{"notifications"}, "GET", "https://api.github.com/notifications?participating=true", true},
		{gh, []string{"notifications"}, "PATCH", "https://api.github.com/notifications/threads/1", true},
		{gh, []string{"notifications"}, "GET", "https://api.github.com/repos/a/b", false},
		{g, []string{"https://www.googleapis.com/auth/calendar.readonly"}, "GET", "https://www.googleapis.com/calendar/v3/users/me/calendarList", true},
		{g, []string{"https://www.googleapis.com/auth/calendar.readonly"}, "POST", "https://www.googleapis.com/calendar/v3/calendars/x/events", false},
		{g, []string{"https://www.googleapis.com/auth/calendar.readonly"}, "GET", "https://gmail.googleapis.com/gmail/v1/users/me/messages", false},
		{g, []string{"https://www.googleapis.com/auth/gmail.readonly"}, "GET", "https://gmail.googleapis.com/gmail/v1/users/me/messages", true},
	} {
		if ok, _ := c.p.covers(c.scopes, c.method, mustURL(t, c.url)); ok != c.want {
			t.Errorf("%s %s %s with %v: covered=%v, want %v", c.p.Name, c.method, c.url, c.scopes, ok, c.want)
		}
	}
}

func TestProviderManifestChecks(t *testing.T) {
	base := `
name = "x"
title = "X"
[sign_in]
flow = "device"
device_url = "https://x.example.com/d"
token_url = "https://x.example.com/t"
revoke = "none"
[scopes.covers]
a = ["https://api.example.com/"]
`
	if _, err := parseProvider([]byte(base)); err != nil {
		t.Fatalf("a good manifest: %v", err)
	}
	for name, m := range map[string]string{
		"unknown field":       base + "\nsurprise = 1\n",
		"http endpoint":       strings.Replace(base, "https://x.example.com/t", "http://x.example.com/t", 1),
		"bad flow":            strings.Replace(base, `flow = "device"`, `flow = "carrier-pigeon"`, 1),
		"bad revoke":          strings.Replace(base, `revoke = "none"`, `revoke = "please"`, 1),
		"github-grant, no id": strings.Replace(base, `revoke = "none"`, `revoke = "github-grant"`+"\nrevoke_url = \"https://api.example.com/g\"", 1),
		"no covers":           strings.Replace(base, "[scopes.covers]\na = [\"https://api.example.com/\"]\n", "", 1),
		"plain http prefix":   strings.Replace(base, `a = ["https://api.example.com/"]`, `a = ["http://api.example.com/"]`, 1),
		"lowercase method":    strings.Replace(base, `a = ["https://api.example.com/"]`, `a = ["get https://api.example.com/"]`, 1),
	} {
		if _, err := parseProvider([]byte(m)); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}

// A: a widget declaring a scope its provider doesn't list is refused.
func TestUnknownScopeRefusedAtLoad(t *testing.T) {
	spec := testSpec(func(context.Context, widget.Host) error { return nil })
	spec.Uses = []widget.Use{{Provider: "github", Scopes: []string{"notifications", "repo:everything"}}}
	_, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec}})
	if err == nil || !strings.Contains(err.Error(), "repo:everything") {
		t.Fatalf("err = %v, want the unknown scope named", err)
	}
}

// D: with one token shared, a widget is signed only for what its own
// scopes cover.
func TestLeastPrivilegeOnSharedToken(t *testing.T) {
	h := newAccountHarness(t)
	h.workForBoth()
	ia := h.srv.instances["ia"] // declares a: /me
	if _, _, _, err := h.srv.authorization(context.Background(), ia, "GET", mustURL(t, "https://api.example.com/me")); err != nil {
		t.Fatalf("its own scope: %v", err)
	}
	_, _, _, err := h.srv.authorization(context.Background(), ia, "GET", mustURL(t, "https://api.example.com/b/me"))
	var se *widget.ScopeError
	if !errors.As(err, &se) || !slices.Equal(se.Missing, []string{"b"}) || se.Refused {
		t.Fatalf("borrowing wb's scope: %v, want a ScopeError naming b", err)
	}
	if _, _, _, err := h.srv.authorization(context.Background(), ia, "DELETE", mustURL(t, "https://api.example.com/me")); err == nil {
		t.Fatal("a GET-only scope must not sign a DELETE")
	}
}

func TestMissingScopeStrategies(t *testing.T) {
	ps, _ := loadBuiltinProviders()
	resp := func(status int, hdr map[string]string) *http.Response {
		r := &http.Response{StatusCode: status, Header: http.Header{}}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	g, gh := ps["google"], ps["github"]
	// RFC 6750.
	if got := g.missingScopes(resp(403, map[string]string{"WWW-Authenticate": `Bearer realm="x", error="insufficient_scope", scope="s1 s2"`})); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Errorf("rfc6750: %v", got)
	}
	if got := g.missingScopes(resp(401, map[string]string{"WWW-Authenticate": `Bearer error="invalid_token"`})); got != nil {
		t.Errorf("an expired token isn't a scope problem: %v", got)
	}
	// GitHub's headers.
	if got := gh.missingScopes(resp(404, map[string]string{"X-OAuth-Scopes": "notifications", "X-Accepted-OAuth-Scopes": "repo"})); !slices.Equal(got, []string{"repo"}) {
		t.Errorf("headers: %v", got)
	}
	if got := gh.missingScopes(resp(404, map[string]string{"X-OAuth-Scopes": "notifications, repo", "X-Accepted-OAuth-Scopes": "repo"})); got != nil {
		t.Errorf("had an accepted scope, so not a scope problem: %v", got)
	}
	if got := gh.missingScopes(resp(200, map[string]string{"X-OAuth-Scopes": "notifications", "X-Accepted-OAuth-Scopes": "repo"})); got != nil {
		t.Errorf("a success isn't a scope problem: %v", got)
	}
}

// C: a signed request the API refuses for a missing scope fails with a
// ScopeError, and flags the instance (which keeps running).
func TestUndeclaredScopeFlagsInstance(t *testing.T) {
	h := newAccountHarness(t)
	h.newAccount("ia", "me@work", "Work")
	h.waitState("ia", widgetdv1.InstanceStatus_STATE_RUNNING)
	ia := h.srv.instances["ia"]
	rh := &runHost{in: ia, ctx: context.Background(), cards: map[string]*card{}}
	req, _ := http.NewRequest("GET", "https://api.example.com/needs-c", nil)
	_, err := rh.Fetch(widget.Authorize(req))
	var se *widget.ScopeError
	if !errors.As(err, &se) || !se.Refused || !slices.Equal(se.Missing, []string{"c"}) {
		t.Fatalf("err = %v, want a refused ScopeError naming c", err)
	}
	st := ia.status()
	if !strings.Contains(st.GetWarning(), "didn't declare: c") || st.GetState() != widgetdv1.InstanceStatus_STATE_RUNNING {
		t.Fatalf("status = %v: want running, with a warning", st)
	}
}

// B: a refused sign-in stops only the widgets asking for what was refused.
func TestRejectedScopeQuarantinesItsWidget(t *testing.T) {
	cSpec := widget.Spec{
		Name: "wc", Version: "1",
		Files:   []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Config:  &testwidgetv1.Config{},
		View:    testSpec(nil).View,
		Run:     func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() },
		Network: widget.NetworkRequest{Hosts: []string{"api.example.com"}},
		Uses:    []widget.Use{{Provider: "idp", Scopes: []string{"c"}}},
	}
	h := newAccountHarness(t, cSpec)
	work := h.workForBoth() // a and b
	in, _ := h.srv.addInstance("wc")
	if err := h.srv.configure(in.id, "", nil); err != nil {
		t.Fatal(err)
	}
	h.bind(in.id, work)
	asked := &pendingSignIn{provider: "idp", account: work, scopes: []string{"a", "b", "c", "openid"}}

	// The provider names c.
	h.srv.consentRejected(asked, "Some requested scopes were invalid. {invalid=[c]}")
	if st := in.status(); st.GetState() != widgetdv1.InstanceStatus_STATE_FAULTED || !strings.Contains(st.GetMessage(), "refused: c") {
		t.Fatalf("wc = %v, want stopped, naming c", st)
	}
	h.srv.mu.Lock()
	want := h.srv.wantedLocked(h.acct(work))
	h.srv.mu.Unlock()
	if !slices.Equal(want, []string{"a", "b"}) {
		t.Fatalf("the union still has the refused scope: %v", want)
	}
	if h.state("ia") != widgetdv1.InstanceStatus_STATE_RUNNING {
		t.Fatal("widgets not asking for c must carry on")
	}
	// Saving its settings lifts the quarantine.
	if err := h.srv.configure(in.id, "", nil); err != nil {
		t.Fatal(err)
	}
	if in.status().GetState() == widgetdv1.InstanceStatus_STATE_FAULTED {
		t.Fatal("saving its settings should lift the quarantine")
	}

	// The provider says nothing: the newcomer (c, added since the last good
	// sign-in) is suspect.
	h.srv.consentRejected(asked, "")
	if st := in.status(); st.GetState() != widgetdv1.InstanceStatus_STATE_FAULTED {
		t.Fatalf("an unnamed refusal should fall on the newcomer: %v", st)
	}
}

func TestRejectedWithNothingToBlame(t *testing.T) {
	h := newAccountHarness(t)
	// No sign-in yet: every scope is new, so nothing can be pinned down.
	h.srv.consentRejected(&pendingSignIn{provider: "idp", instance: "ia", scopes: []string{"a", "openid"}}, "")
	if h.srv.instances["ia"].quarantine != "" || h.srv.instances["ib"].quarantine != "" {
		t.Fatal("with nothing to compare against, no one should be quarantined")
	}
	if !strings.Contains(h.srv.accountError("idp"), "didn't say which scope") {
		t.Fatalf("account error = %q", h.srv.accountError("idp"))
	}
}
