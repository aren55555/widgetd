package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	abiv1 "github.com/aren55555/widgetd/gen/widgetd/abi/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	// wasmMemoryPages caps each module's memory: 128 MiB in 64 KiB pages.
	wasmMemoryPages = 2048
	// maxCallBytes caps one encoded Call read from a module's memory.
	maxCallBytes = 8 << 20
	// maxLogLine caps a line a module writes to stdout or stderr.
	maxLogLine = 4 << 10
)

// wasmHost runs sandboxed widgets. One wazero runtime is shared; every
// instance is its own module, with its own memory, and nothing but the two
// widgetd imports and a minimal WASI (clock, sleep, random) to reach out with.
type wasmHost struct {
	rt wazero.Runtime
}

type guestKey struct{}

func newWasmHost(ctx context.Context, cacheDir string) (*wasmHost, error) {
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(wasmMemoryPages)
	if cacheDir != "" {
		// Compiling a Go module to native code takes seconds on a small
		// board; the cache makes every start after the first instant.
		if cache, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
			cfg = cfg.WithCompilationCache(cache)
		}
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	_, err := rt.NewHostModuleBuilder("widgetd").
		NewFunctionBuilder().WithFunc(hostCall).Export("call").
		NewFunctionBuilder().WithFunc(hostTake).Export("take").
		Instantiate(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	return &wasmHost{rt: rt}, nil
}

func (w *wasmHost) compile(ctx context.Context, bin []byte) (wazero.CompiledModule, error) {
	cm, err := w.rt.CompileModule(ctx, bin)
	if err != nil {
		return nil, err
	}
	for _, def := range cm.ImportedFunctions() {
		mod, fn, _ := def.Import()
		if mod != "widgetd" && mod != wasi_snapshot_preview1.ModuleName {
			cm.Close(ctx)
			return nil, fmt.Errorf("module imports %s.%s, which the host doesn't provide", mod, fn)
		}
	}
	return cm, nil
}

// run executes one instance's module until it exits or ctx is done. Every
// call it makes is served by h, the same Host a native widget gets, so the
// schema checks, storage and network policy are exactly the same.
func (w *wasmHost) run(ctx context.Context, cm wazero.CompiledModule, name string, h *runHost) error {
	g := &guest{h: h, cards: map[string]*rawCard{}}
	out := &logWriter{log: h.Logger(), level: slog.LevelInfo}
	errOut := &logWriter{log: h.Logger(), level: slog.LevelWarn}
	defer out.Flush()
	defer errOut.Flush()
	cfg := wazero.NewModuleConfig().
		WithName(""). // anonymous: many instances of one widget at once
		WithArgs(name).
		WithStartFunctions("_start").
		WithSysWalltime().
		WithSysNanotime().
		// Sleep wakes when the instance is stopped, so an idle widget
		// (most of them, most of the time) doesn't hold up shutdown.
		WithNanosleep(func(ns int64) {
			t := time.NewTimer(time.Duration(ns))
			defer t.Stop()
			select {
			case <-t.C:
			case <-ctx.Done():
			}
		}).
		WithRandSource(rand.Reader).
		WithStdout(out).
		WithStderr(errOut)
	mod, err := w.rt.InstantiateModule(context.WithValue(ctx, guestKey{}, g), cm, cfg)
	if mod != nil {
		mod.Close(context.Background())
	}
	var exit *sys.ExitError
	switch {
	case err == nil:
		return errors.New("module exited")
	case errors.As(err, &exit) && ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &exit):
		return fmt.Errorf("module exited with code %d", exit.ExitCode())
	}
	return err
}

// guest is the host's view of one running module: the Host it is served by,
// the cards it mounted, and the Result it has yet to take.
type guest struct {
	h       *runHost
	mu      sync.Mutex
	pending []byte
	cards   map[string]*rawCard
}

func hostCall(ctx context.Context, m api.Module, ptr, n uint32) uint32 {
	g := ctx.Value(guestKey{}).(*guest)
	res := &abiv1.Result{}
	if n > maxCallBytes {
		res.Error = fmt.Sprintf("call of %d bytes is over the %d limit", n, maxCallBytes)
	} else if b, ok := m.Memory().Read(ptr, n); !ok {
		res.Error = "call outside the module's memory"
	} else {
		var call abiv1.Call
		if err := proto.Unmarshal(b, &call); err != nil {
			res.Error = "malformed call: " + err.Error()
		} else {
			res = g.dispatch(ctx, &call)
		}
	}
	out, err := proto.Marshal(res)
	if err != nil {
		out, _ = proto.Marshal(&abiv1.Result{Error: err.Error()})
	}
	g.mu.Lock()
	g.pending = out
	g.mu.Unlock()
	return uint32(len(out))
}

func hostTake(ctx context.Context, m api.Module, ptr, n uint32) {
	g := ctx.Value(guestKey{}).(*guest)
	g.mu.Lock()
	out := g.pending
	g.pending = nil
	g.mu.Unlock()
	if uint32(len(out)) != n || !m.Memory().Write(ptr, out) {
		// A guest that takes the wrong length or out of bounds is broken;
		// stop it rather than hand it a partial result.
		panic(fmt.Sprintf("widgetd.take: want %d bytes at %d, have %d", n, ptr, len(out)))
	}
}

func errResult(err error) *abiv1.Result {
	var pd *PolicyDenied
	if errors.As(err, &pd) {
		return &abiv1.Result{Error: err.Error(), DeniedRule: pd.Rule}
	}
	return &abiv1.Result{Error: err.Error()}
}

func (g *guest) dispatch(ctx context.Context, call *abiv1.Call) *abiv1.Result {
	h := g.h
	switch op := call.GetOp().(type) {
	case *abiv1.Call_ConfigGet:
		h.in.mu.Lock()
		cfg := h.in.config
		h.in.mu.Unlock()
		return &abiv1.Result{Value: &abiv1.Result_Config{Config: cfg}}

	case *abiv1.Call_Fetch:
		return g.fetch(ctx, op.Fetch)

	case *abiv1.Call_KvGet:
		v, found, err := h.KV().Get(op.KvGet.GetKey())
		if err != nil {
			return errResult(err)
		}
		return &abiv1.Result{Value: &abiv1.Result_Kv{Kv: &abiv1.KvValue{Found: found, Value: v}}}

	case *abiv1.Call_KvPut:
		if err := h.KV().Put(op.KvPut.GetKey(), op.KvPut.GetValue()); err != nil {
			return errResult(err)
		}
		return &abiv1.Result{}

	case *abiv1.Call_KvDelete:
		if err := h.KV().Delete(op.KvDelete.GetKey()); err != nil {
			return errResult(err)
		}
		return &abiv1.Result{}

	case *abiv1.Call_Mount:
		m := op.Mount
		opts := widget.MountOptions{
			Title:      m.GetTitle(),
			Layout:     widget.Layout{Cols: m.GetCols(), Rows: m.GetRows()},
			StateType:  protoreflect.FullName(m.GetStateType()),
			DeltaType:  protoreflect.FullName(m.GetDeltaType()),
			ActionType: protoreflect.FullName(m.GetActionType()),
		}
		if ms := m.GetVisibleUntilMs(); ms > 0 {
			opts.VisibleUntil = time.UnixMilli(ms)
		}
		rc, err := h.Mount(m.GetId(), opts)
		if err != nil {
			return errResult(err)
		}
		g.mu.Lock()
		g.cards[m.GetId()] = rc.(*rawCard)
		g.mu.Unlock()
		return &abiv1.Result{}

	case *abiv1.Call_Unmount:
		g.mu.Lock()
		delete(g.cards, op.Unmount.GetId())
		g.mu.Unlock()
		if err := h.Unmount(op.Unmount.GetId()); err != nil {
			return errResult(err)
		}
		return &abiv1.Result{}

	case *abiv1.Call_Emit:
		e := op.Emit
		g.mu.Lock()
		rc := g.cards[e.GetCardId()]
		g.mu.Unlock()
		if rc == nil {
			return &abiv1.Result{Error: "card " + e.GetCardId() + " is not mounted"}
		}
		var want bool
		var err error
		switch e.GetKind() {
		case abiv1.Emit_KIND_SNAPSHOT:
			err = rc.Snapshot(e.GetValue())
		case abiv1.Emit_KIND_DELTA:
			want, err = rc.deltaDeferred(e.GetValue())
		case abiv1.Emit_KIND_CHECKPOINT:
			err = rc.checkpoint(e.GetValue())
		default:
			err = fmt.Errorf("emit kind %v", e.GetKind())
		}
		if err != nil {
			return errResult(err)
		}
		return &abiv1.Result{Value: &abiv1.Result_Emit{Emit: &abiv1.EmitResult{WantCheckpoint: want}}}

	case *abiv1.Call_TakeActions:
		out := &abiv1.Actions{}
		g.mu.Lock()
		for id, rc := range g.cards {
			ch := rc.Actions()
			for ch != nil {
				select {
				case b, ok := <-ch:
					if !ok {
						ch = nil
						break
					}
					out.Actions = append(out.Actions, &abiv1.CardAction{CardId: id, Value: b})
					continue
				default:
				}
				break
			}
		}
		g.mu.Unlock()
		return &abiv1.Result{Value: &abiv1.Result_Actions{Actions: out}}

	case *abiv1.Call_Log:
		l := op.Log
		var attrs []any
		for _, a := range l.GetAttrs() {
			attrs = append(attrs, a.GetName(), a.GetValue())
		}
		level := map[abiv1.Log_Level]slog.Level{
			abiv1.Log_LEVEL_DEBUG: slog.LevelDebug, abiv1.Log_LEVEL_WARN: slog.LevelWarn, abiv1.Log_LEVEL_ERROR: slog.LevelError,
		}[l.GetLevel()]
		h.Logger().Log(ctx, level, truncate(l.GetMessage(), maxLogLine), attrs...)
		return &abiv1.Result{}
	}
	return &abiv1.Result{Error: "unknown call"}
}

func (g *guest) fetch(ctx context.Context, f *abiv1.Fetch) *abiv1.Result {
	req, err := http.NewRequestWithContext(ctx, f.GetMethod(), f.GetUrl(), bytes.NewReader(f.GetBody()))
	if err != nil {
		return errResult(err)
	}
	for _, hd := range f.GetHeaders() {
		req.Header.Add(hd.GetName(), hd.GetValue())
	}
	resp, err := g.h.Fetch(req)
	if err != nil {
		return errResult(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errResult(err)
	}
	out := &abiv1.FetchResponse{Status: int32(resp.StatusCode), Body: body}
	for name, vs := range resp.Header {
		for _, v := range vs {
			out.Headers = append(out.Headers, &abiv1.Header{Name: name, Value: v})
		}
	}
	return &abiv1.Result{Value: &abiv1.Result_Fetch{Fetch: out}}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// logWriter turns a module's stdout/stderr into log lines.
type logWriter struct {
	log   *slog.Logger
	level slog.Level
	mu    sync.Mutex
	buf   []byte
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLogLine {
		w.emit(w.buf)
		w.buf = nil
	}
	return len(p), nil
}

func (w *logWriter) emit(line []byte) {
	s := strings.TrimRight(string(line), "\r")
	if s != "" {
		w.log.Log(context.Background(), w.level, "widget output", "line", truncate(s, maxLogLine))
	}
}

func (w *logWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}
