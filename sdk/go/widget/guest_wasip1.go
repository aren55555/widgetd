//go:build wasip1

package widget

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
	"unsafe"

	"google.golang.org/protobuf/proto"

	abiv1 "github.com/aren55555/widgetd/gen/widgetd/abi/v1"
)

//go:wasmimport widgetd call
func hostCall(ptr unsafe.Pointer, n uint32) uint32

//go:wasmimport widgetd take
func hostTake(ptr unsafe.Pointer, n uint32)

// Main runs a widget's server half inside the sandbox. A sandboxed widget's
// main package is just:
//
//	func main() { widget.Main(server.Run) }
//
// run receives the same Host interface it would get compiled into a host,
// so the widget's code doesn't change between the two.
func Main(run func(ctx context.Context, h Host) error, opts ...MainOption) {
	g := &guestHost{}
	var o mainOptions
	for _, opt := range opts {
		opt(&o)
	}
	if len(os.Args) > 1 && os.Args[1] == "widgetd:choices" {
		os.Exit(g.answerChoices(o.choices))
	}
	if err := run(context.Background(), g); err != nil {
		// Say why first: the host shows it on every screen, where an exit
		// code alone would only say that something went wrong.
		g.call(&abiv1.Call{Op: &abiv1.Call_Exit{Exit: &abiv1.Exit{Error: err.Error()}}})
		fmt.Fprintln(os.Stderr, "widget:", err)
		os.Exit(1)
	}
}

// PolicyError is what Fetch returns when the host's network policy refuses
// a request. Rule says which rule.
type PolicyError struct {
	Rule string
	Msg  string
}

func (e *PolicyError) Error() string { return e.Msg }

type guestHost struct {
	mu sync.Mutex // one call/take pair at a time

	// Actions: one poller for the module, one channel per card that asked.
	pollOnce sync.Once
	actMu    sync.Mutex
	actChans map[string]chan []byte
}

func (g *guestHost) call(c *abiv1.Call) (*abiv1.Result, error) {
	in, err := proto.Marshal(c)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	var p unsafe.Pointer
	if len(in) > 0 {
		p = unsafe.Pointer(&in[0])
	}
	n := hostCall(p, uint32(len(in)))
	out := make([]byte, n)
	var q unsafe.Pointer
	if n > 0 {
		q = unsafe.Pointer(&out[0])
	}
	hostTake(q, n)
	g.mu.Unlock()

	var res abiv1.Result
	if err := proto.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	if res.GetDeniedRule() != "" {
		return &res, &PolicyError{Rule: res.GetDeniedRule(), Msg: res.GetError()}
	}
	if res.GetError() != "" {
		return &res, errors.New(res.GetError())
	}
	return &res, nil
}

func (g *guestHost) Config(dst proto.Message) error {
	res, err := g.call(&abiv1.Call{Op: &abiv1.Call_ConfigGet{ConfigGet: &abiv1.ConfigGet{}}})
	if err != nil {
		return err
	}
	return proto.Unmarshal(res.GetConfig(), dst)
}

func (g *guestHost) Fetch(req *http.Request) (*http.Response, error) {
	f := &abiv1.Fetch{Method: req.Method, Url: req.URL.String()}
	for name, vs := range req.Header {
		for _, v := range vs {
			f.Headers = append(f.Headers, &abiv1.Header{Name: name, Value: v})
		}
	}
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		f.Body = b
	}
	res, err := g.call(&abiv1.Call{Op: &abiv1.Call_Fetch{Fetch: f}})
	if err != nil {
		return nil, err
	}
	fr := res.GetFetch()
	resp := &http.Response{
		Status:     fmt.Sprintf("%d %s", fr.GetStatus(), http.StatusText(int(fr.GetStatus()))),
		StatusCode: int(fr.GetStatus()),
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{},
		Body:          io.NopCloser(bytes.NewReader(fr.GetBody())),
		ContentLength: int64(len(fr.GetBody())),
		Request:       req,
	}
	for _, h := range fr.GetHeaders() {
		resp.Header.Add(h.GetName(), h.GetValue())
	}
	return resp, nil
}

func (g *guestHost) KV() KV { return guestKV{g} }

func (g *guestHost) Mount(id string, opts MountOptions) (RawCard, error) {
	m := &abiv1.Mount{
		Id: id, Title: opts.Title, Cols: opts.Layout.Cols, Rows: opts.Layout.Rows,
		StateType: string(opts.StateType), DeltaType: string(opts.DeltaType),
		ActionType: string(opts.ActionType),
	}
	if !opts.VisibleUntil.IsZero() {
		m.VisibleUntilMs = opts.VisibleUntil.UnixMilli()
	}
	if _, err := g.call(&abiv1.Call{Op: &abiv1.Call_Mount{Mount: m}}); err != nil {
		return nil, err
	}
	return &guestCard{g: g, id: id}, nil
}

func (g *guestHost) Unmount(id string) error {
	_, err := g.call(&abiv1.Call{Op: &abiv1.Call_Unmount{Unmount: &abiv1.Unmount{Id: id}}})
	return err
}

func (g *guestHost) Logger() *slog.Logger { return slog.New(&guestLog{g: g}) }

type guestKV struct{ g *guestHost }

func (k guestKV) Get(key string) ([]byte, bool, error) {
	res, err := k.g.call(&abiv1.Call{Op: &abiv1.Call_KvGet{KvGet: &abiv1.KvGet{Key: key}}})
	if err != nil {
		return nil, false, err
	}
	return res.GetKv().GetValue(), res.GetKv().GetFound(), nil
}

func (k guestKV) Put(key string, value []byte) error {
	_, err := k.g.call(&abiv1.Call{Op: &abiv1.Call_KvPut{KvPut: &abiv1.KvPut{Key: key, Value: value}}})
	return err
}

func (k guestKV) Delete(key string) error {
	_, err := k.g.call(&abiv1.Call{Op: &abiv1.Call_KvDelete{KvDelete: &abiv1.KvDelete{Key: key}}})
	return err
}

type guestCard struct {
	g  *guestHost
	id string
}

func (c *guestCard) emit(kind abiv1.Emit_Kind, b []byte) (*abiv1.Result, error) {
	return c.g.call(&abiv1.Call{Op: &abiv1.Call_Emit{Emit: &abiv1.Emit{CardId: c.id, Kind: kind, Value: b}}})
}

func (c *guestCard) Snapshot(state []byte) error {
	_, err := c.emit(abiv1.Emit_KIND_SNAPSHOT, state)
	return err
}

// Delta sends the change, then, if the host asks, the full current state:
// the host can't call back into the module, so compaction is two calls.
func (c *guestCard) Delta(delta []byte, current func() ([]byte, error)) error {
	res, err := c.emit(abiv1.Emit_KIND_DELTA, delta)
	if err != nil {
		return err
	}
	if !res.GetEmit().GetWantCheckpoint() {
		return nil
	}
	state, err := current()
	if err != nil {
		return err
	}
	_, err = c.emit(abiv1.Emit_KIND_CHECKPOINT, state)
	return err
}

// guestLog is a slog.Handler that hands records to the host's logger.
type guestLog struct {
	g     *guestHost
	attrs []slog.Attr
}

func (l *guestLog) Enabled(context.Context, slog.Level) bool { return true }

func (l *guestLog) Handle(_ context.Context, r slog.Record) error {
	level := abiv1.Log_LEVEL_INFO
	switch {
	case r.Level >= slog.LevelError:
		level = abiv1.Log_LEVEL_ERROR
	case r.Level >= slog.LevelWarn:
		level = abiv1.Log_LEVEL_WARN
	case r.Level < slog.LevelInfo:
		level = abiv1.Log_LEVEL_DEBUG
	}
	msg := &abiv1.Log{Level: level, Message: r.Message}
	add := func(a slog.Attr) bool {
		msg.Attrs = append(msg.Attrs, &abiv1.Header{Name: a.Key, Value: a.Value.String()})
		return true
	}
	for _, a := range l.attrs {
		add(a)
	}
	r.Attrs(add)
	_, err := l.g.call(&abiv1.Call{Op: &abiv1.Call_Log{Log: msg}})
	return err
}

func (l *guestLog) WithAttrs(as []slog.Attr) slog.Handler {
	return &guestLog{g: l.g, attrs: append(append([]slog.Attr{}, l.attrs...), as...)}
}

func (l *guestLog) WithGroup(string) slog.Handler { return l }

// actionPoll is how often a module asks for viewers' actions. The host
// can't call in, so it's a poll; 200ms reads as instant to a person.
const actionPoll = 200 * time.Millisecond

// Actions returns the card's actions channel, starting the module's poller
// the first time any card asks.
func (c *guestCard) Actions() <-chan []byte {
	g := c.g
	g.actMu.Lock()
	if g.actChans == nil {
		g.actChans = map[string]chan []byte{}
	}
	ch, ok := g.actChans[c.id]
	if !ok {
		ch = make(chan []byte, 16)
		g.actChans[c.id] = ch
	}
	g.actMu.Unlock()
	g.pollOnce.Do(func() { go g.pollActions() })
	return ch
}

func (g *guestHost) pollActions() {
	for {
		time.Sleep(actionPoll)
		res, err := g.call(&abiv1.Call{Op: &abiv1.Call_TakeActions{TakeActions: &abiv1.TakeActions{}}})
		if err != nil {
			continue
		}
		g.actMu.Lock()
		for _, a := range res.GetActions().GetActions() {
			if ch, ok := g.actChans[a.GetCardId()]; ok {
				select {
				case ch <- a.GetValue():
				default: // the widget isn't keeping up; drop, as the host would
				}
			}
		}
		g.actMu.Unlock()
	}
}

// answerChoices is choices mode: run the widget's ChoicesFunc, if it has
// one, send the answer, and say how to exit.
func (g *guestHost) answerChoices(f ChoicesFunc) int {
	out := &abiv1.Choices{}
	if f != nil {
		m, err := f(context.Background(), g)
		if err != nil {
			g.call(&abiv1.Call{Op: &abiv1.Call_Exit{Exit: &abiv1.Exit{Error: err.Error()}}})
			return 1
		}
		for field, cs := range m {
			fc := &abiv1.FieldChoices{Field: field}
			for _, c := range cs {
				fc.Choices = append(fc.Choices, &abiv1.Choice{Value: c.Value, Label: c.Label})
			}
			out.Fields = append(out.Fields, fc)
		}
	}
	if _, err := g.call(&abiv1.Call{Op: &abiv1.Call_Choices{Choices: out}}); err != nil {
		return 1
	}
	return 0
}
