package host

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/gen/widgetd/v1/widgetdv1connect"
	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

func (h *harness) adminClient() widgetdv1connect.WidgetAdminServiceClient {
	return widgetdv1connect.NewWidgetAdminServiceClient(http.DefaultClient, h.http.URL)
}

// nextInstance returns the next instance-status frame for id.
func (f *frames) nextInstance(id string) *widgetdv1.InstanceStatus {
	f.t.Helper()
	for {
		fr := f.nextAny()
		if fr.GetInstanceId() == id && fr.GetInstance() != nil {
			return fr.GetInstance()
		}
	}
}

func cfgBytes(t *testing.T, label string) []byte {
	t.Helper()
	b, err := proto.Marshal(&testwidgetv1.Config{Label: label})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ticketOf(t *testing.T, url string) string {
	t.Helper()
	_, tok, ok := strings.Cut(url, "/widgetd/configure/")
	if !ok || tok == "" {
		t.Fatalf("ticket url = %q", url)
	}
	return tok
}

func TestConfigureByTicket(t *testing.T) {
	h := newHarness(t)
	h.host() // the seeded instance
	ctx := context.Background()
	c := h.adminClient()
	device, admin := h.deviceCookie("d1"), h.adminCookie()
	f := h.open(device)
	if st := f.nextInstance("one"); st.GetState() != widgetdv1.InstanceStatus_STATE_RUNNING {
		t.Fatalf("seeded instance = %v", st)
	}

	// A screen adds a placeholder; every screen hears about it.
	added, err := c.AddInstance(ctx, withCookie(&widgetdv1.AddInstanceRequest{Widget: "test"}, device))
	if err != nil {
		t.Fatal(err)
	}
	id := added.Msg.GetInstanceId()
	if st := f.nextInstance(id); st.GetState() != widgetdv1.InstanceStatus_STATE_NEEDS_CONFIG {
		t.Fatalf("new instance = %v", st)
	}
	if _, err := c.AddInstance(ctx, connect.NewRequest(&widgetdv1.AddInstanceRequest{Widget: "test"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous add: %v", err)
	}

	// The screen gets a ticket to show as a QR code...
	tk, err := c.RequestConfigTicket(ctx, withCookie(&widgetdv1.RequestConfigTicketRequest{InstanceId: id}, device))
	if err != nil {
		t.Fatal(err)
	}
	ticket := ticketOf(t, tk.Msg.GetUrl())

	// ...but the screen itself can't use it: saving needs a passkey.
	if _, err := c.GetConfigForm(ctx, withCookie(&widgetdv1.GetConfigFormRequest{Ticket: ticket}, device)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("device reading the form: %v", err)
	}
	if _, err := c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{Ticket: ticket, Config: cfgBytes(t, "ok")}, device)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("device saving: %v", err)
	}

	// The phone renders the form from the schema it's given.
	form, err := c.GetConfigForm(ctx, withCookie(&widgetdv1.GetConfigFormRequest{Ticket: ticket}, admin))
	if err != nil {
		t.Fatal(err)
	}
	if form.Msg.GetConfigType() != "testwidget.v1.Config" || len(form.Msg.GetSchema()) == 0 || form.Msg.GetInstanceId() != id {
		t.Fatalf("form = %v", form.Msg)
	}

	// A config breaking a rule comes back as a field error and saves nothing.
	bad, err := c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{Ticket: ticket, Config: cfgBytes(t, "far too long")}, admin))
	if err != nil {
		t.Fatal(err)
	}
	if v := bad.Msg.GetViolations(); len(v) != 1 || v[0].GetField() != "label" {
		t.Fatalf("violations = %v", v)
	}

	// A good one starts the instance with it.
	good, err := c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{Ticket: ticket, Label: "Kitchen", Config: cfgBytes(t, "ok")}, admin))
	if err != nil || len(good.Msg.GetViolations()) != 0 {
		t.Fatalf("save = %v, %v", good, err)
	}
	var cfg testwidgetv1.Config
	if err := h.host().Config(&cfg); err != nil || cfg.GetLabel() != "ok" {
		t.Fatalf("started with %v, %v", &cfg, err)
	}
	if st := f.nextInstance(id); st.GetState() != widgetdv1.InstanceStatus_STATE_RUNNING || st.GetTitle() != "Kitchen" {
		t.Fatalf("after save = %v", st)
	}

	// The ticket was single-use.
	if _, err := c.GetConfigForm(ctx, withCookie(&widgetdv1.GetConfigFormRequest{Ticket: ticket}, admin)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("used ticket: %v", err)
	}
}

// The admin page edits an instance's settings by id, without a ticket.
func TestConfigureByID(t *testing.T) {
	h := newHarness(t)
	h.host() // the seeded instance, "one"
	ctx := context.Background()
	c := h.adminClient()
	device, admin := h.deviceCookie("d1"), h.adminCookie()
	f := h.open(device)
	f.nextInstance("one")

	// Only an admin may, and only for an instance that exists.
	if _, err := c.GetConfigForm(ctx, withCookie(&widgetdv1.GetConfigFormRequest{InstanceId: "one"}, device)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("device reading the form: %v", err)
	}
	if _, err := c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{InstanceId: "one", Config: cfgBytes(t, "dev")}, device)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("device saving: %v", err)
	}
	if _, err := c.GetConfigForm(ctx, withCookie(&widgetdv1.GetConfigFormRequest{InstanceId: "nope"}, admin)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown instance: %v", err)
	}
	if _, err := c.GetConfigForm(ctx, withCookie(&widgetdv1.GetConfigFormRequest{}, admin)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("no target: %v", err)
	}

	// The form carries the current configuration...
	form, err := c.GetConfigForm(ctx, withCookie(&widgetdv1.GetConfigFormRequest{InstanceId: "one"}, admin))
	if err != nil {
		t.Fatal(err)
	}
	var cur testwidgetv1.Config
	if err := proto.Unmarshal(form.Msg.GetConfig(), &cur); err != nil || form.Msg.GetInstanceId() != "one" {
		t.Fatalf("form = %v, %v", form.Msg, err)
	}

	// ...rules still apply...
	bad, err := c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{InstanceId: "one", Config: cfgBytes(t, "far too long")}, admin))
	if err != nil || len(bad.Msg.GetViolations()) != 1 {
		t.Fatalf("bad save = %v, %v", bad, err)
	}

	// ...and a good save restarts it with the change, as often as you like.
	for _, v := range []string{"new", "newer"} {
		res, err := c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{InstanceId: "one", Label: "Hall", Config: cfgBytes(t, v)}, admin))
		if err != nil || len(res.Msg.GetViolations()) != 0 {
			t.Fatalf("save %q = %v, %v", v, res, err)
		}
		var cfg testwidgetv1.Config
		if err := h.host().Config(&cfg); err != nil || cfg.GetLabel() != v {
			t.Fatalf("restarted with %v, %v; want %q", &cfg, err, v)
		}
	}
}

func TestRenameAndDelete(t *testing.T) {
	h := newHarness(t)
	wh := h.host()
	ctx := context.Background()
	c := h.adminClient()
	admin := h.adminCookie()
	if err := wh.KV().Put("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	mustMount(t, wh, "main", widget.MountOptions{})
	f := h.open(h.deviceCookie("d1"))
	f.burst()

	if _, err := c.RenameInstance(ctx, withCookie(&widgetdv1.RenameInstanceRequest{InstanceId: "one", Label: "Hall"}, h.deviceCookie("d2"))); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("device renaming: %v", err)
	}
	if _, err := c.RenameInstance(ctx, withCookie(&widgetdv1.RenameInstanceRequest{InstanceId: "one", Label: "Hall"}, admin)); err != nil {
		t.Fatal(err)
	}
	if st := f.nextInstance("one"); st.GetTitle() != "Hall" {
		t.Fatalf("renamed = %v", st)
	}

	if _, err := c.DeleteInstance(ctx, withCookie(&widgetdv1.DeleteInstanceRequest{InstanceId: "one"}, admin)); err != nil {
		t.Fatal(err)
	}
	var purged, removed bool
	for !(purged && removed) {
		fr := f.nextAny()
		if u := fr.GetUnmounted(); u != nil && fr.GetCardId() == "one/main" {
			purged = u.GetPurge()
		}
		if st := fr.GetInstance(); st != nil && st.GetRemoved() {
			removed = true
		}
	}
	if _, ok, _ := (instanceKV{db: h.srv.db, instance: "one"}).Get("k"); ok {
		t.Fatal("a deleted instance's storage should go with it")
	}
	if _, err := c.DeleteInstance(ctx, withCookie(&widgetdv1.DeleteInstanceRequest{InstanceId: "one"}, admin)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("second delete: %v", err)
	}
}

// reopen starts a second host on the same state, as after a restart.
func reopen(t *testing.T, dir string, spec widget.Spec, seeds ...Instance) *Server {
	t.Helper()
	srv, err := New(Options{StateDir: dir, PublicURL: "http://localhost", Widgets: []widget.Spec{spec}, Instances: seeds})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func testSpec(run func(context.Context, widget.Host) error) widget.Spec {
	return widget.Spec{
		Name: "test", Version: "1",
		Files:   []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Config:  &testwidgetv1.Config{},
		View:    fstest.MapFS{"view.js": {Data: []byte("")}},
		Run:     run,
		Network: widget.NetworkRequest{Hosts: []string{"a.example.com", "b.example.com"}},
	}
}

func TestSeedsDontOverwrite(t *testing.T) {
	dir := t.TempDir()
	idle := func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() }
	seed := Instance{ID: "one", Widget: "test", Config: &testwidgetv1.Config{Label: "seed"}}

	s1 := reopen(t, dir, testSpec(idle), seed)
	if err := s1.configure("one", "Mine", cfgBytes(t, "mine")); err != nil {
		t.Fatal(err)
	}
	s1.Close(context.Background())

	s2 := reopen(t, dir, testSpec(idle), seed)
	defer s2.Close(context.Background())
	in := s2.instances["one"]
	var cfg testwidgetv1.Config
	proto.Unmarshal(in.config, &cfg)
	if cfg.GetLabel() != "mine" || in.label != "Mine" {
		t.Fatalf("after restart: config %v, label %q: the seed overwrote a reconfigured instance", &cfg, in.label)
	}
}

func TestStoredConfigInvalidatedByUpgrade(t *testing.T) {
	dir := t.TempDir()
	idle := func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() }
	s1 := reopen(t, dir, testSpec(idle), Instance{ID: "one", Widget: "test"})
	// As if an older version of the widget had allowed it.
	s1.db.Exec(`UPDATE instances SET config = ? WHERE id = 'one'`, cfgBytes(t, "far too long"))
	s1.Close(context.Background())

	s2 := reopen(t, dir, testSpec(idle))
	defer s2.Close(context.Background())
	s2.Start()
	if st := s2.instances["one"].status(); st.GetState() != widgetdv1.InstanceStatus_STATE_INVALID_CONFIG {
		t.Fatalf("state = %v", st)
	}
	if s2.instances["one"].cancel != nil {
		t.Fatal("an instance with an invalid config must not run")
	}
}

func TestNetworkGrant(t *testing.T) {
	dir := t.TempDir()
	idle := func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() }
	srv := reopen(t, dir, testSpec(idle), Instance{ID: "one", Widget: "test"})
	defer srv.Close(context.Background())
	allowed := func(host string) bool {
		return srv.instances["one"].fetcher.policy.Load().check("GET", "x", "https", host, "443") == nil
	}
	if !allowed("a.example.com") || !allowed("b.example.com") {
		t.Fatal("by default, everything requested is granted")
	}
	if err := srv.setGrant("test", []string{"b.example.com"}, false, false); err != nil {
		t.Fatal(err)
	}
	if allowed("a.example.com") || !allowed("b.example.com") {
		t.Fatal("the grant should narrow a running instance's policy at once")
	}
	if err := srv.setGrant("test", []string{"c.example.com"}, false, false); err == nil {
		t.Fatal("granting a host the widget never asked for should be refused")
	}
	srv.Close(context.Background())

	// The grant survives a restart...
	srv = reopen(t, dir, testSpec(idle))
	if allowed("a.example.com") {
		t.Fatal("a stored grant should apply after a restart")
	}
	// ...and resetting it restores the request.
	if err := srv.setGrant("test", nil, false, true); err != nil || !allowed("a.example.com") {
		t.Fatalf("reset: %v", err)
	}
}

func TestExpiredTicket(t *testing.T) {
	tk := newTickets()
	tok, _ := tk.issue("one")
	tk.mu.Lock()
	e := tk.m[tok]
	e.expires = time.Now().Add(-time.Second)
	tk.m[tok] = e
	tk.mu.Unlock()
	if _, ok := tk.lookup(tok); ok {
		t.Fatal("an expired ticket must not resolve")
	}
}

func (h *harness) sendAction(cookie, cardID, typeURL string, b []byte) error {
	c := widgetdv1connect.NewWidgetStreamServiceClient(http.DefaultClient, h.http.URL)
	_, err := c.SendAction(context.Background(), withCookie(&widgetdv1.SendActionRequest{CardId: cardID, TypeUrl: typeURL, Value: b}, cookie))
	return err
}

func TestSendAction(t *testing.T) {
	h := newHarness(t)
	wh := h.host()
	device := h.deviceCookie("d1")
	card, err := widget.Mount[*testwidgetv1.State, *testwidgetv1.Delta](wh, "main", widget.WithAction[*testwidgetv1.Action](widget.MountOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	plain := mustMount(t, wh, "plain", widget.MountOptions{})
	_ = plain
	actions := widget.Actions[*testwidgetv1.Action](card)
	url := "type.googleapis.com/testwidget.v1.Action"
	tap := func(n int64) []byte { b, _ := proto.Marshal(&testwidgetv1.Action{Tap: n}); return b }

	if err := h.sendAction(device, "one/main", url, tap(3)); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-actions:
		if a.GetTap() != 3 {
			t.Fatalf("tap = %d", a.GetTap())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the widget never got the action")
	}

	for _, tc := range []struct {
		name, cookie, card, url string
		b                       []byte
		want                    connect.Code
	}{
		{"unpaired", "", "one/main", url, tap(1), connect.CodeUnauthenticated},
		{"breaks its rule", device, "one/main", url, tap(-1), connect.CodeInvalidArgument},
		{"wrong type", device, "one/main", "type.googleapis.com/testwidget.v1.State", nil, connect.CodeInvalidArgument},
		{"card takes none", device, "one/plain", url, tap(1), connect.CodeFailedPrecondition},
		{"no such card", device, "one/nope", url, tap(1), connect.CodeNotFound},
	} {
		if err := h.sendAction(tc.cookie, tc.card, tc.url, tc.b); connect.CodeOf(err) != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

// Every route the phone app handles must reach it. (The configure page once
// 404ed: the app knew the route and the server didn't.)
func TestPhonePagesAreServed(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"enroll/tok", "pair/id", "configure/ticket", "edit/id", "admin"} {
		resp, err := http.Get(h.http.URL + "/widgetd/" + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("/widgetd/%s: %d %s", p, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	// And every route the app's router names is one of them.
	src, err := os.ReadFile("../client/src/admin.tsx")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`section === "([a-z]+)"`).FindAllStringSubmatch(string(src), -1) {
		if !phonePages[m[1]] {
			t.Errorf("admin.tsx routes %q, which the server doesn't serve", m[1])
		}
	}
}

func TestConfigChoicesRPC(t *testing.T) {
	calls := 0
	spec := testSpec(func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() })
	spec.Choices = func(ctx context.Context, h widget.Host) (map[string][]widget.Choice, error) {
		calls++
		var cfg testwidgetv1.Config
		h.Config(&cfg)
		return map[string][]widget.Choice{"label": {{Value: cfg.GetLabel() + "?"}}}, nil
	}
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec},
		Instances: []Instance{{ID: "one", Widget: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	srv.Start()
	t.Cleanup(func() { h.http.Close(); srv.Close(context.Background()) })

	c := h.adminClient()
	ctx := context.Background()
	if _, err := c.GetConfigChoices(ctx, withCookie(&widgetdv1.GetConfigChoicesRequest{InstanceId: "one", Config: cfgBytes(t, "a")}, h.deviceCookie("d1"))); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a screen asking for choices: %v", err)
	}
	admin := h.adminCookie()
	for range 2 {
		res, err := c.GetConfigChoices(ctx, withCookie(&widgetdv1.GetConfigChoicesRequest{InstanceId: "one", Config: cfgBytes(t, "a")}, admin))
		if err != nil {
			t.Fatal(err)
		}
		if f := res.Msg.GetFields(); len(f) != 1 || f[0].GetChoices()[0].GetValue() != "a?" || f[0].GetChoices()[0].GetLabel() != "a?" {
			t.Fatalf("fields = %v", f)
		}
	}
	if calls != 1 {
		t.Fatalf("the widget was asked %d times for the same settings; want once (cached)", calls)
	}
}

func TestSaveRunsVerify(t *testing.T) {
	spec := testSpec(func(ctx context.Context, _ widget.Host) error { <-ctx.Done(); return ctx.Err() })
	spec.Verify = func(ctx context.Context, h widget.Host) (widget.Verdict, error) {
		var cfg testwidgetv1.Config
		h.Config(&cfg)
		if cfg.GetLabel() == "nope" {
			return widget.Verdict{Violations: []widget.Violation{{Field: "label", Message: "no such thing"}}}, nil
		}
		if cfg.GetLabel() == "down" {
			return widget.Verdict{}, errors.New("upstream is down")
		}
		return widget.Verdict{Config: &testwidgetv1.Config{Label: strings.ToUpper(cfg.GetLabel())}}, nil
	}
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec},
		Instances: []Instance{{ID: "one", Widget: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	srv.Start()
	t.Cleanup(func() { h.http.Close(); srv.Close(context.Background()) })
	c, admin, ctx := h.adminClient(), h.adminCookie(), context.Background()
	save := func(label string) (*connect.Response[widgetdv1.SaveConfigResponse], error) {
		return c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{InstanceId: "one", Config: cfgBytes(t, label)}, admin))
	}

	res, err := save("nope")
	if err != nil || len(res.Msg.GetViolations()) != 1 || res.Msg.GetViolations()[0].GetMessage() != "no such thing" {
		t.Fatalf("objection: %v, %v", res, err)
	}
	if _, err := save("down"); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "upstream is down") {
		t.Fatalf("check failed: %v", err)
	}
	if res, err := save("ok"); err != nil || len(res.Msg.GetViolations()) != 0 {
		t.Fatalf("approval: %v, %v", res, err)
	}
	var cfg testwidgetv1.Config
	proto.Unmarshal(srv.instances["one"].config, &cfg)
	if cfg.GetLabel() != "OK" {
		t.Fatalf("saved %q, want the widget's tidied OK", cfg.GetLabel())
	}
}
