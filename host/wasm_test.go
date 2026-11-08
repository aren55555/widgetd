package host

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

var (
	wasmOnce  sync.Once
	wasmBin   []byte
	wasmErr   error
	wasmCache = filepath.Join(os.TempDir(), "widgetd-test-wasm-cache")
)

// testWasm builds the test widget for the sandbox, once per test run.
func testWasm(t *testing.T) []byte {
	t.Helper()
	wasmOnce.Do(func() {
		dir, err := os.MkdirTemp("", "widgetd-wasm")
		if err != nil {
			wasmErr = err
			return
		}
		out := filepath.Join(dir, "w.wasm")
		cmd := exec.Command("go", "build", "-o", out, "../internal/testwidget/wasm")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if b, err := cmd.CombinedOutput(); err != nil {
			wasmErr = err
			t.Logf("%s", b)
			return
		}
		wasmBin, wasmErr = os.ReadFile(out)
	})
	if wasmErr != nil {
		t.Fatalf("build test widget: %v", wasmErr)
	}
	return wasmBin
}

// newWasmHarness runs the sandboxed test widget, one instance per label.
func newWasmHarness(t *testing.T, labels ...string) *harness {
	t.Helper()
	spec := widget.Spec{
		Name:    "test",
		Version: "1",
		Files:   []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto},
		Config:  &testwidgetv1.Config{},
		View:    fstest.MapFS{"view.js": {Data: []byte("/* view */")}},
		Wasm:    testWasm(t),
		Network: widget.NetworkRequest{Hosts: []string{"api.example.com"}},
	}
	var instances []Instance
	for _, l := range labels {
		instances = append(instances, Instance{ID: l, Widget: "test", Config: &testwidgetv1.Config{Label: l}})
	}
	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec}, Instances: instances, wasmCacheDir: wasmCache})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	srv.Start()
	t.Cleanup(func() {
		h.http.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return h
}

// waitState returns the first snapshot of card id whose state satisfies ok.
func (f *frames) waitState(id string, ok func(*testwidgetv1.State) bool) *testwidgetv1.State {
	f.t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-deadline:
			f.t.Fatalf("no matching state for %s", id)
		default:
		}
		fr := f.next()
		if fr.GetCardId() == id && fr.GetSnapshot() != nil {
			if s := decodeState(f.t, fr.GetSnapshot()); ok(s) {
				return s
			}
		}
	}
}

func TestWasmStreamsAndCheckpoints(t *testing.T) {
	h := newWasmHarness(t, "basic")
	f := h.open(h.deviceCookie("d1"))
	// Run long enough to pass the log's length, so a checkpoint has been
	// asked for and given.
	var last *widgetdv1.Payload
	deltas := 0
	for deltas < maxDeltas+10 {
		fr := f.next()
		if d := fr.GetDelta(); d != nil {
			last = d
			deltas++
		}
	}
	f.cancel()

	// A fresh client now gets the checkpoint and only the deltas after it,
	// not the hundreds since the widget started.
	f2 := h.open(h.deviceCookie("d2"))
	b := f2.burst()
	if len(b) < 2 || b[1].GetSnapshot() == nil {
		t.Fatalf("burst = %v", b)
	}
	snap := decodeState(t, b[1].GetSnapshot())
	if int64(len(snap.GetValues()))+int64(len(b)-2) < int64(last.GetCursor().GetSeq())-1 {
		t.Fatalf("snapshot (%d values) plus %d deltas doesn't reach seq %d", len(snap.GetValues()), len(b)-2, last.GetCursor().GetSeq())
	}
	if len(b)-2 > maxDeltas {
		t.Fatalf("%d deltas after the snapshot: the checkpoint wasn't taken", len(b)-2)
	}
}

func TestWasmKVIsPerInstance(t *testing.T) {
	h := newWasmHarness(t, "kvput", "kvget")
	f := h.open(h.deviceCookie("d1"))
	got := f.collect(func(s *testwidgetv1.State) bool { return len(s.GetValues()) == 1 }, "kvput/main", "kvget/main")
	put, get := got["kvput/main"], got["kvget/main"]
	if put.GetValues()[0] != 1 || get.GetValues()[0] != 0 {
		t.Fatalf("kvput saw its own key: %v; kvget saw kvput's: %v", put.GetValues(), get.GetValues())
	}
}

func TestWasmFetchIsPolicied(t *testing.T) {
	h := newWasmHarness(t, "fetch")
	f := h.open(h.deviceCookie("d1"))
	s := f.waitState("fetch/main", func(s *testwidgetv1.State) bool { return len(s.GetValues()) == 1 })
	if s.GetValues()[0] != 1 {
		t.Fatal("a fetch outside the request should come back as a PolicyError")
	}
	acts := h.srv.instances["fetch"].fetcher.Activity()
	if len(acts) != 1 || acts[0].Denied == "" {
		t.Fatalf("activity = %+v", acts)
	}
}

func TestWasmPayloadsAreChecked(t *testing.T) {
	h := newWasmHarness(t, "bad")
	f := h.open(h.deviceCookie("d1"))
	s := f.waitState("bad/main", func(s *testwidgetv1.State) bool { return len(s.GetValues()) == 1 })
	if s.GetValues()[0] != 1 {
		t.Fatal("a delta breaking its rule should be refused inside the sandbox too")
	}
}

func TestWasmMemoryHogIsStoppedAlone(t *testing.T) {
	h := newWasmHarness(t, "alloc", "kvput")
	f := h.open(h.deviceCookie("d1"))
	f.waitState("kvput/main", func(s *testwidgetv1.State) bool { return len(s.GetValues()) == 1 })

	// The hog hits the memory cap and its module dies; the host restarts it
	// with backoff, while the other instance carries on.
	deadline := time.Now().Add(30 * time.Second)
	for {
		h.srv.board.mu.Lock()
		_, kv := h.srv.board.cards["kvput/main"]
		h.srv.board.mu.Unlock()
		if !kv {
			t.Fatal("the well-behaved instance was taken down")
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
		if h.srv.instances["alloc"].restarts() > 0 {
			return
		}
	}
	t.Fatal("the memory hog was never stopped")
}

func TestWasmSpinnerStopsOnClose(t *testing.T) {
	h := newWasmHarness(t, "spin")
	f := h.open(h.deviceCookie("d1"))
	f.waitState("spin/main", func(*testwidgetv1.State) bool { return true })
	// Cleanup's Close must interrupt the loop; it fails the test if not.
}

// collect waits until every card in ids has sent a state satisfying ok,
// whatever order they arrive in.
func (f *frames) collect(ok func(*testwidgetv1.State) bool, ids ...string) map[string]*testwidgetv1.State {
	f.t.Helper()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	got := map[string]*testwidgetv1.State{}
	deadline := time.Now().Add(30 * time.Second)
	for len(got) < len(want) {
		if time.Now().After(deadline) {
			f.t.Fatalf("states so far: %v", got)
		}
		fr := f.next()
		if want[fr.GetCardId()] && fr.GetSnapshot() != nil {
			if s := decodeState(f.t, fr.GetSnapshot()); ok(s) {
				got[fr.GetCardId()] = s
			}
		}
	}
	return got
}

// A packaged widget runs from nothing but its .wpkg: dynamic descriptors for
// the schema, the module, the view. Validation rules must survive the trip.
func TestPackageRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	m := Manifest{Name: "pkgd", Version: "2", Config: "testwidget.v1.Config",
		Network: widget.NetworkRequest{Hosts: []string{"api.example.com"}}}
	view := fstest.MapFS{"view.js": {Data: []byte("/* v */")}, "view.css": {Data: []byte("p{}")}}
	if err := WritePackage(&buf, m, []protoreflect.FileDescriptor{testwidgetv1.File_testwidget_v1_testwidget_proto}, testWasm(t), view); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadPackage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "pkgd" || spec.Config == nil || len(spec.Files) != 1 || spec.Network.Hosts[0] != "api.example.com" {
		t.Fatalf("spec = %+v", spec)
	}
	if _, err := fs.Stat(spec.View, "view.css"); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec},
		Instances:    []Instance{{ID: "bad", Widget: "pkgd", Config: &testwidgetv1.Config{Label: "bad"}}},
		wasmCacheDir: wasmCache})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	srv.Start()
	t.Cleanup(func() { h.http.Close(); srv.Close(context.Background()) })

	f := h.open(h.deviceCookie("d1"))
	s := f.waitState("bad/main", func(s *testwidgetv1.State) bool { return len(s.GetValues()) == 1 })
	if s.GetValues()[0] != 1 {
		t.Fatal("rules from a packaged schema must still be enforced")
	}
	if _, err := New(Options{StateDir: t.TempDir(), PublicURL: "http://localhost", Widgets: []widget.Spec{spec},
		Instances: []Instance{{ID: "x", Widget: "pkgd", Config: &testwidgetv1.Config{Label: "far too long"}}}}); err == nil {
		t.Fatal("a config breaking its rule should be refused at load")
	}
}

func TestPackageRejectsJunk(t *testing.T) {
	if _, err := LoadPackage([]byte("not a zip")); err == nil {
		t.Fatal("junk should be refused")
	}
}

func TestWasmActions(t *testing.T) {
	h := newWasmHarness(t, "act")
	device := h.deviceCookie("d1")
	f := h.open(device)
	f.waitState("act/act", func(*testwidgetv1.State) bool { return true })
	b, _ := proto.Marshal(&testwidgetv1.Action{Tap: 7})
	if err := h.sendAction(device, "act/act", "type.googleapis.com/testwidget.v1.Action", b); err != nil {
		t.Fatal(err)
	}
	f.waitState("act/act", func(s *testwidgetv1.State) bool { return len(s.GetValues()) == 1 && s.GetValues()[0] == 7 })
}

// A sandboxed widget's error is what screens see, not just its exit code.
func TestWasmExitReason(t *testing.T) {
	h := newWasmHarness(t, "fail")
	f := h.open(h.deviceCookie("d1"))
	for {
		st := f.nextInstance("fail")
		if st.GetState() == widgetdv1.InstanceStatus_STATE_FAULTED {
			if st.GetMessage() != "deliberately failed" {
				t.Fatalf("message = %q", st.GetMessage())
			}
			return
		}
	}
}

func TestWasmChoices(t *testing.T) {
	h := newWasmHarness(t, "basic")
	in := h.srv.instances["basic"]
	fields, err := h.srv.configChoices(context.Background(), in, cfgBytes(t, "hi"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 || fields[0].GetField() != "label" {
		t.Fatalf("fields = %v", fields)
	}
	cs := fields[0].GetChoices()
	if cs[0].GetValue() != "hi!" || cs[0].GetLabel() != "echo" {
		t.Fatalf("choices should be built from the unsaved settings: %v", cs)
	}
	if cs[1].GetValue() != "true" {
		t.Fatal("a widget offering choices must not be able to mount a card")
	}
	// Garbage settings are refused before the module runs.
	if _, err := h.srv.configChoices(context.Background(), in, []byte{0xff, 0xff}); err == nil {
		t.Fatal("settings that don't decode should be refused")
	}
}
