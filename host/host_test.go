package host

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

// harness runs a host with one instance of the test widget. The widget's Run
// hands its Host to the test and waits, so each test drives it directly.
type harness struct {
	t     *testing.T
	srv   *Server
	http  *httptest.Server
	hosts chan widget.Host
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, hosts: make(chan widget.Host, 4)}
	spec := widget.Spec{
		Name:    "test",
		Version: "1",
		Files:   []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Config:  &testwidgetv1.Config{},
		View:    fstest.MapFS{"view.js": {Data: []byte("/* view */")}},
		Run: func(ctx context.Context, wh widget.Host) error {
			h.hosts <- wh
			<-ctx.Done()
			return ctx.Err()
		},
	}
	srv, err := New(Options{
		StateDir:  t.TempDir(),
		PublicURL: "http://localhost",
		Widgets:   []widget.Spec{spec},
		Instances: []Instance{{ID: "one", Widget: "test", Config: &testwidgetv1.Config{Label: "hi"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h.srv = srv
	h.http = httptest.NewServer(mux)
	srv.Start()
	t.Cleanup(func() {
		h.http.Close()
		srv.Close(context.Background())
	})
	return h
}

func (h *harness) host() widget.Host {
	h.t.Helper()
	select {
	case wh := <-h.hosts:
		return wh
	case <-time.After(5 * time.Second):
		h.t.Fatal("widget never started")
		return nil
	}
}

// deviceCookie pairs a screen the short way: a session row straight in the DB.
func (h *harness) deviceCookie(id string) string {
	h.t.Helper()
	if _, err := h.srv.db.Exec(`INSERT INTO devices (id, name, paired_at, last_seen) VALUES (?, 'screen', 0, 0)`, id); err != nil {
		h.t.Fatal(err)
	}
	ck, err := h.srv.auth.startSession(widgetdv1.Role_ROLE_DEVICE, id, time.Hour, false)
	if err != nil {
		h.t.Fatal(err)
	}
	return ck.Name + "=" + ck.Value
}

type frames struct {
	t      *testing.T
	stream *connect.ServerStreamForClient[widgetdv1.StreamWidgetsResponse]
	cancel context.CancelFunc
}

func (h *harness) open(cookie string, resume ...*widgetdv1.Resume) *frames {
	h.t.Helper()
	client := widgetdv1connect.NewWidgetStreamServiceClient(http.DefaultClient, h.http.URL)
	ctx, cancel := context.WithCancel(context.Background())
	req := connect.NewRequest(&widgetdv1.StreamWidgetsRequest{Resume: resume})
	if cookie != "" {
		req.Header().Set("Cookie", cookie)
	}
	s, err := client.StreamWidgets(ctx, req)
	if err != nil {
		cancel()
		h.t.Fatal(err)
	}
	h.t.Cleanup(cancel)
	return &frames{t: h.t, stream: s, cancel: cancel}
}

// next returns the next card frame: not a keepalive, not an instance status.
func (f *frames) next() *widgetdv1.WidgetFrame {
	f.t.Helper()
	for {
		fr := f.nextAny()
		if fr.GetInstance() == nil {
			return fr
		}
	}
}

// nextAny returns the next non-keepalive frame.
func (f *frames) nextAny() *widgetdv1.WidgetFrame {
	f.t.Helper()
	for {
		got := make(chan bool, 1)
		go func() { got <- f.stream.Receive() }()
		select {
		case ok := <-got:
			if !ok {
				f.t.Fatalf("stream ended: %v", f.stream.Err())
			}
			if fr := f.stream.Msg().GetFrame(); fr != nil {
				return fr
			}
		case <-time.After(30 * time.Second): // generous: a first module compile under -race is slow
			f.t.Fatal("no frame within 30s")
		}
	}
}

// burst reads the initial frames up to the end-of-burst keepalive.
func (f *frames) burst() []*widgetdv1.WidgetFrame {
	f.t.Helper()
	var out []*widgetdv1.WidgetFrame
	for {
		if !f.stream.Receive() {
			f.t.Fatalf("stream ended: %v", f.stream.Err())
		}
		fr := f.stream.Msg().GetFrame()
		if fr == nil {
			return out
		}
		if fr.GetInstance() != nil {
			continue // instance statuses: see TestInstanceStatusFrames
		}
		out = append(out, fr)
	}
}

func mustMount(t *testing.T, wh widget.Host, id string, opts widget.MountOptions) *widget.Card[*testwidgetv1.State, *testwidgetv1.Delta] {
	t.Helper()
	c, err := widget.Mount[*testwidgetv1.State, *testwidgetv1.Delta](wh, id, opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func decodeState(t *testing.T, p *widgetdv1.Payload) *testwidgetv1.State {
	t.Helper()
	var s testwidgetv1.State
	if err := proto.Unmarshal(p.GetValue(), &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestUnpairedScreenIsRefused(t *testing.T) {
	h := newHarness(t)
	client := widgetdv1connect.NewWidgetStreamServiceClient(http.DefaultClient, h.http.URL)
	s, err := client.StreamWidgets(context.Background(), connect.NewRequest(&widgetdv1.StreamWidgetsRequest{}))
	if err == nil {
		for s.Receive() {
		}
		err = s.Err()
	}
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("err = %v, want unauthenticated", err)
	}
}

func TestConfigReachesWidget(t *testing.T) {
	h := newHarness(t)
	var cfg testwidgetv1.Config
	if err := h.host().Config(&cfg); err != nil || cfg.GetLabel() != "hi" {
		t.Fatalf("config = %v, %v", &cfg, err)
	}
}

func TestStreamAndResume(t *testing.T) {
	h := newHarness(t)
	wh := h.host()
	cookie := h.deviceCookie("d1")

	state := &testwidgetv1.State{Values: []int64{1}}
	card := mustMount(t, wh, "main", widget.MountOptions{Title: "Test"})
	if err := card.Snapshot(state); err != nil {
		t.Fatal(err)
	}

	// First visit: Mounted, then the snapshot.
	f := h.open(cookie)
	b := f.burst()
	if len(b) != 2 || b[0].GetMounted() == nil || b[1].GetSnapshot() == nil {
		t.Fatalf("first burst = %v", b)
	}
	if m := b[0].GetMounted(); m.GetStateType() != "testwidget.v1.State" || m.GetDeltaType() != "testwidget.v1.Delta" || b[0].GetCardId() != "one/main" {
		t.Fatalf("mounted = %v", m)
	}
	snapCursor := b[1].GetSnapshot().GetCursor()
	digest := b[0].GetMounted().GetWidget().GetDigest()

	// Live deltas arrive on the open stream.
	for _, v := range []int64{2, 3} {
		state.Values = append(state.Values, v)
		if err := card.Delta(&testwidgetv1.Delta{Value: v}, func() *testwidgetv1.State { return state }); err != nil {
			t.Fatal(err)
		}
		if d := f.next().GetDelta(); d == nil {
			t.Fatal("want a live delta")
		}
	}
	f.cancel()

	// Reconnect holding the snapshot's cursor: only the two deltas.
	f2 := h.open(cookie, &widgetdv1.Resume{CardId: "one/main", Digest: digest, Cursor: snapCursor})
	b2 := f2.burst()
	if len(b2) != 3 || b2[0].GetMounted() == nil || b2[1].GetDelta() == nil || b2[2].GetDelta() == nil {
		t.Fatalf("resumed burst = %v", b2)
	}
	if b2[1].GetDelta().GetPrev().GetSeq() != snapCursor.GetSeq() {
		t.Fatal("first delta should apply on top of the client's cursor")
	}

	// A cursor from another build is ignored: snapshot again.
	f3 := h.open(cookie, &widgetdv1.Resume{CardId: "one/main", Digest: "other", Cursor: snapCursor})
	if b3 := f3.burst(); b3[1].GetSnapshot() == nil {
		t.Fatalf("other-digest burst = %v", b3)
	}
}

func TestPayloadsAreCheckedAgainstSchema(t *testing.T) {
	h := newHarness(t)
	wh := h.host()
	f := h.open(h.deviceCookie("d1"))
	f.burst()

	card := mustMount(t, wh, "main", widget.MountOptions{})
	f.next() // mounted
	state := &testwidgetv1.State{}
	if err := card.Snapshot(state); err != nil {
		t.Fatal(err)
	}
	f.next() // snapshot

	// Breaks `int64.gte = 0`.
	if err := card.Delta(&testwidgetv1.Delta{Value: -1}, func() *testwidgetv1.State { return state }); err == nil {
		t.Fatal("a delta breaking its rule should be refused")
	}
	if fr := f.next(); fr.GetFault() == nil {
		t.Fatalf("want a fault frame, got %v", fr)
	}
	// Breaks `string.max_len = 8`.
	if err := card.Snapshot(&testwidgetv1.State{Label: "far too long"}); err == nil {
		t.Fatal("a snapshot breaking its rule should be refused")
	}

	// Raw bytes that aren't the declared type at all.
	raw, err := wh.Mount("raw", widget.MountOptions{StateType: "testwidget.v1.State"})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Snapshot([]byte{0xff, 0xff, 0xff}); err == nil {
		t.Fatal("garbage bytes should be refused")
	}
	// A type that isn't in the widget's schema.
	if _, err := wh.Mount("x", widget.MountOptions{StateType: "widgetd.v1.Mounted"}); err == nil {
		t.Fatal("a type outside the widget's schema should be refused")
	}
}

func TestVisibleUntilUnmounts(t *testing.T) {
	h := newHarness(t)
	wh := h.host()
	f := h.open(h.deviceCookie("d1"))
	f.burst()

	mustMount(t, wh, "brief", widget.MountOptions{VisibleUntil: time.Now().Add(200 * time.Millisecond)})
	if f.next().GetMounted().GetVisibleUntil() == nil {
		t.Fatal("mounted should carry visible_until")
	}
	if fr := f.next(); fr.GetUnmounted() == nil || fr.GetCardId() != "one/brief" {
		t.Fatalf("want the card unmounted by the host, got %v", fr)
	}
	if _, err := wh.Mount("past", widget.MountOptions{StateType: "testwidget.v1.State", VisibleUntil: time.Now().Add(-time.Second)}); err == nil {
		t.Fatal("mounting already-expired should be refused")
	}
}

func TestKVIsPerInstance(t *testing.T) {
	h := newHarness(t)
	kv := h.host().KV()
	if err := kv.Put("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	other := instanceKV{db: h.srv.db, instance: "two"}
	if _, ok, _ := other.Get("k"); ok {
		t.Fatal("another instance must not see this one's keys")
	}
	if v, ok, _ := kv.Get("k"); !ok || string(v) != "v" {
		t.Fatalf("get = %q, %v", v, ok)
	}
}

func TestRevokedDeviceIsCutOff(t *testing.T) {
	h := newHarness(t)
	f := h.open(h.deviceCookie("d1"))
	f.burst()

	h.srv.db.Exec(`DELETE FROM sessions WHERE subject = 'd1'`)
	h.srv.auth.kick("d1")
	for f.stream.Receive() {
	}
	if connect.CodeOf(f.stream.Err()) != connect.CodeUnauthenticated {
		t.Fatalf("err = %v, want unauthenticated", f.stream.Err())
	}
}

func TestRestartGetsNewEpoch(t *testing.T) {
	h := newHarness(t)
	wh := h.host()
	card := mustMount(t, wh, "main", widget.MountOptions{})
	card.Snapshot(&testwidgetv1.State{})

	f := h.open(h.deviceCookie("d1"))
	b := f.burst()
	epoch := b[1].GetSnapshot().GetCursor().GetEpoch()

	// Make the run return; the host restarts it after its backoff.
	h.srv.instances["one"].w.spec.Run = func(ctx context.Context, wh widget.Host) error {
		h.hosts <- wh
		<-ctx.Done()
		return ctx.Err()
	}
	cancelRun(t, wh)
	if fr := f.next(); fr.GetUnmounted() == nil {
		t.Fatalf("a stopped run's cards should unmount, got %v", fr)
	}
	wh2 := h.host()
	if err := card.Snapshot(&testwidgetv1.State{}); err == nil {
		t.Fatal("a card from the previous run must not be able to emit")
	}
	c2 := mustMount(t, wh2, "main", widget.MountOptions{})
	c2.Snapshot(&testwidgetv1.State{})
	f.next() // mounted
	if got := f.next().GetSnapshot().GetCursor().GetEpoch(); got == epoch {
		t.Fatal("a restarted instance should have a new epoch")
	}
}

// cancelRun ends the current run of the instance behind wh.
func cancelRun(t *testing.T, wh widget.Host) {
	t.Helper()
	rh, ok := wh.(*runHost)
	if !ok {
		t.Fatal("not a runHost")
	}
	rh.mu.Lock()
	defer rh.mu.Unlock()
	if rh.cancel == nil {
		t.Fatal(errors.New("runHost has no cancel"))
	}
	rh.cancel()
}
