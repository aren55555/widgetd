package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

const (
	// maxDeltas is how many deltas a card's log holds before the host asks
	// the widget for a fresh snapshot.
	maxDeltas = 120
	// actionBuffer is how many actions a card holds for a widget that's busy.
	actionBuffer = 16
	// minBackoff and maxBackoff bound the backoff between restarts of a failing instance.
	minBackoff = time.Second
	maxBackoff = 5 * time.Minute
	// healthyAfter is how long a run must last to reset the backoff.
	healthyAfter = time.Minute
)

var cardIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// instance is a copy of a widget, configured or waiting to be.
type instance struct {
	id       string
	w        *loadedWidget
	srv      *Server
	log      *slog.Logger
	fetcher  *fetcher
	created  time.Time
	restartN atomic.Int64

	mu         sync.Mutex
	label      string
	config     []byte // as stored: validated, secrets removed
	runConfig  []byte // what the widget sees: config with its secrets back
	configured bool
	state      widgetdv1.InstanceStatus_State
	message    string
	// With STATE_NEEDS_SIGN_IN: the provider, and the account (if one is
	// bound) it waits for.
	waitProvider, waitAccount, waitLabel string
	bindings                             map[string]string // provider → account id (see accounts.go)
	warning                              string            // something wrong that doesn't stop it
	quarantine                           string            // set: stopped, and out of its accounts' grants (see accounts.go)

	// Set while running; guarded by srv.mu.
	cancel context.CancelFunc
	done   chan struct{}
	// deleting makes the run's final unmount a purge: screens drop what
	// they cached for it.
	deleting atomic.Bool
}

// run keeps the instance's Run going until ctx is done, restarting it with
// backoff when it returns or panics. Each run gets its own epoch and its own
// Host, so anything a previous run left behind can no longer emit.
func (in *instance) run(ctx context.Context) {
	backoff := minBackoff
	for {
		in.setState(widgetdv1.InstanceStatus_STATE_RUNNING, "")
		started := time.Now()
		h := &runHost{in: in, epoch: uint64(started.UnixNano()), cards: map[string]*card{}}
		runCtx, cancel := context.WithCancel(ctx)
		h.ctx, h.cancel = runCtx, cancel //nolint:fatcontext // a fresh child of ctx each run, not a nested one
		err := in.safeRun(runCtx, h)
		cancel()
		h.close()
		in.srv.board.unmountInstance(in.id, in.deleting.Load())
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > healthyAfter {
			backoff = minBackoff
		}
		in.log.Warn("widget instance stopped; restarting", "err", err, "in", backoff)
		in.restartN.Add(1)
		in.setState(widgetdv1.InstanceStatus_STATE_FAULTED, firstLine(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (in *instance) safeRun(ctx context.Context, h *runHost) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	if in.w.spec.Wasm != nil {
		cm, err := in.w.compiled(in.srv.wasm)
		if err != nil {
			return fmt.Errorf("compile: %w", err)
		}
		return in.srv.wasm.run(ctx, cm, in.w.spec.Name, h)
	}
	err = in.w.spec.Run(ctx, h)
	if err == nil {
		err = errors.New("the widget's Run returned")
	}
	return err
}

// runHost is the widget.Host for one run of one instance.
type runHost struct {
	in     *instance
	epoch  uint64
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	cards  map[string]*card // by local id

	// helper marks a run in choices mode: it offers settings choices and
	// must not show anything. Its Config is helperConfig, the unsaved
	// settings being edited.
	helper       bool
	helperConfig []byte
}

func (h *runHost) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, c := range h.cards {
		if c.actions != nil {
			close(c.actions)
			c.actions = nil
		}
	}
}

// deliver hands a viewer's action to the card's widget without waiting: a
// widget that isn't keeping up loses actions rather than holding up the
// screen that sent them.
func (h *runHost) deliver(c *card, b []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || c.actions == nil || h.cards[localID(c)] != c {
		return false
	}
	select {
	case c.actions <- b:
		return true
	default:
		return false
	}
}

func localID(c *card) string { return strings.TrimPrefix(c.id, c.instanceID+"/") }

func (h *runHost) Config(dst proto.Message) error {
	if h.helper {
		return proto.Unmarshal(h.helperConfig, dst)
	}
	cfg := h.in.w.spec.Config
	if cfg == nil {
		return errors.New("widget declares no Config")
	}
	if dst.ProtoReflect().Descriptor().FullName() != cfg.ProtoReflect().Descriptor().FullName() {
		return fmt.Errorf("config is %s, not %s", cfg.ProtoReflect().Descriptor().FullName(), dst.ProtoReflect().Descriptor().FullName())
	}
	h.in.mu.Lock()
	b := h.in.runConfig
	h.in.mu.Unlock()
	return proto.Unmarshal(b, dst)
}

func (h *runHost) Fetch(req *http.Request) (*http.Response, error) {
	if err := h.ctx.Err(); err != nil {
		return nil, err
	}
	var auth, provider, acct string
	if widget.IsAuthorized(req) {
		a, p, ac, err := h.in.srv.authorization(h.ctx, h.in, req.Method, req.URL)
		if err != nil {
			h.flagScope(err)
			return nil, err
		}
		auth, provider, acct = a, p, ac
	}
	resp, err := h.in.fetcher.do(h.ctx, req, auth)
	if err == nil && provider != "" {
		// The API may say the request needed a scope the token lacks: the
		// widget used one it didn't declare. Fail it, and flag the widget.
		p := h.in.srv.providers[provider]
		if missing := p.missingScopes(resp); len(missing) > 0 {
			resp.Body.Close()
			se := &widget.ScopeError{Provider: p.Title, Missing: missing, Refused: true}
			h.flagScope(se)
			return nil, se
		}
	}
	if err == nil && auth != "" && resp.StatusCode == http.StatusUnauthorized && !h.helper {
		// The provider stopped accepting the account's token: forget it and
		// put its widgets back to waiting; from another goroutine, since that
		// stops this run and stopping waits for it.
		go h.in.srv.accountLapsed(acct)
	}
	return resp, err
}

func (h *runHost) KV() widget.KV { return instanceKV{db: h.in.srv.db, instance: h.in.id} }

func (h *runHost) Logger() *slog.Logger { return h.in.log }

func (h *runHost) Mount(id string, opts widget.MountOptions) (widget.RawCard, error) {
	if h.helper {
		return nil, errors.New("a widget offering settings choices can't show cards")
	}
	if !cardIDRE.MatchString(id) {
		return nil, fmt.Errorf("card id %q: want letters, digits, '.', '_' or '-', at most 64", id)
	}
	sch := h.in.w.schema
	if _, err := sch.message(opts.StateType); err != nil {
		return nil, fmt.Errorf("card %s state: %w", id, err)
	}
	if opts.DeltaType != "" {
		if _, err := sch.message(opts.DeltaType); err != nil {
			return nil, fmt.Errorf("card %s delta: %w", id, err)
		}
	}
	if opts.ActionType != "" {
		if _, err := sch.message(opts.ActionType); err != nil {
			return nil, fmt.Errorf("card %s action: %w", id, err)
		}
	}
	if !opts.VisibleUntil.IsZero() && !opts.VisibleUntil.After(time.Now()) {
		return nil, fmt.Errorf("card %s: visible_until is already past", id)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errStopped
	}
	w := h.in.w
	c := &card{
		instanceID: h.in.id,
		id:         h.in.id + "/" + id,
		epoch:      h.epoch,
		digest:     w.digest,
		stateURL:   typeURL(opts.StateType),
		deltaURL:   typeURL(opts.DeltaType),
		actionURL:  typeURL(opts.ActionType),
		host:       h,
		log:        newCardLog(h.epoch, maxDeltas),
		mounted: &widgetdv1.Mounted{
			Widget:     &widgetdv1.WidgetRef{Name: w.spec.Name, Version: w.spec.Version, Digest: w.digest},
			FrameUrl:   h.in.srv.frameURL(w),
			StateType:  string(opts.StateType),
			DeltaType:  string(opts.DeltaType),
			ActionType: string(opts.ActionType),
			Layout:     &widgetdv1.Layout{Cols: max(opts.Layout.Cols, 1), Rows: max(opts.Layout.Rows, 1)},
			Title:      opts.Title,
			Icon:       iconProto(w.spec.Icon),
			ShortId:    shortHash(h.in.id + "/" + id),
		},
	}
	// Re-mounting with the same types keeps the card's data; anything else
	// starts it over.
	if old, ok := h.cards[id]; ok && old.stateURL == c.stateURL && old.deltaURL == c.deltaURL {
		c.log = old.log
	}
	if c.actionURL != "" {
		c.actions = make(chan []byte, actionBuffer)
		if old, ok := h.cards[id]; ok && old.actions != nil {
			close(old.actions)
		}
	}
	if !opts.VisibleUntil.IsZero() {
		c.mounted.VisibleUntil = timestamppb.New(opts.VisibleUntil)
		board := h.in.srv.board
		c.expiry = time.AfterFunc(time.Until(opts.VisibleUntil), func() { board.unmount(c.id, c, false) })
	}
	h.cards[id] = c
	h.in.srv.board.mount(c)
	return &rawCard{h: h, c: c, opts: opts}, nil
}

func (h *runHost) Unmount(id string) error {
	h.mu.Lock()
	c, ok := h.cards[id]
	delete(h.cards, id)
	if ok && c.actions != nil {
		close(c.actions)
		c.actions = nil
	}
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("card %s is not mounted", id)
	}
	h.in.srv.board.unmount(c.id, c, false)
	return nil
}

// rawCard checks every payload against the widget's schema before it can
// reach a screen.
type rawCard struct {
	h    *runHost
	c    *card
	opts widget.MountOptions
}

var errGone = errors.New("card is no longer mounted")

func (r *rawCard) live() error {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	if r.h.closed {
		return errStopped
	}
	return nil
}

func (r *rawCard) refuse(err error) error {
	r.h.in.log.Error("widget sent a payload that breaks its schema", "card", r.c.id, "err", err)
	r.h.in.srv.board.fault(r.c, "the widget sent data that doesn't match its schema")
	return err
}

func (r *rawCard) Snapshot(state []byte) error {
	if err := r.live(); err != nil {
		return err
	}
	if err := r.h.in.w.schema.check(r.opts.StateType, state); err != nil {
		return r.refuse(err)
	}
	if !r.h.in.srv.board.snapshot(r.c, state) {
		return errGone
	}
	return nil
}

func (r *rawCard) Delta(delta []byte, current func() ([]byte, error)) error {
	if err := r.live(); err != nil {
		return err
	}
	if r.opts.DeltaType == "" {
		return errors.New("card was mounted without a Delta type")
	}
	sch := r.h.in.w.schema
	if err := sch.check(r.opts.DeltaType, delta); err != nil {
		return r.refuse(err)
	}
	checked := func() ([]byte, error) {
		b, err := current()
		if err != nil {
			return nil, err
		}
		if err := sch.check(r.opts.StateType, b); err != nil {
			return nil, r.refuse(err)
		}
		return b, nil
	}
	ok, err := r.h.in.srv.board.delta(r.c, delta, checked)
	if err != nil {
		return err
	}
	if !ok {
		return errGone
	}
	return nil
}

// deltaDeferred sends a delta and reports whether a checkpoint should follow.
func (r *rawCard) deltaDeferred(delta []byte) (bool, error) {
	if err := r.live(); err != nil {
		return false, err
	}
	if r.opts.DeltaType == "" {
		return false, errors.New("card was mounted without a Delta type")
	}
	if err := r.h.in.w.schema.check(r.opts.DeltaType, delta); err != nil {
		return false, r.refuse(err)
	}
	want, ok, err := r.h.in.srv.board.deltaDeferred(r.c, delta)
	if !ok {
		return false, errGone
	}
	return want, err
}

// checkpoint stores the full current state, after a delta asked for it.
func (r *rawCard) checkpoint(state []byte) error {
	if err := r.live(); err != nil {
		return err
	}
	if err := r.h.in.w.schema.check(r.opts.StateType, state); err != nil {
		return r.refuse(err)
	}
	if !r.h.in.srv.board.checkpoint(r.c, state) {
		return errGone
	}
	return nil
}

// restarts is how many times the instance has stopped and been restarted.
func (in *instance) restarts() int64 { return in.restartN.Load() }

// firstLine is an error's first line: enough to show on a card, without a
// panic's stack trace.
func firstLine(err error) string {
	if err == nil {
		return ""
	}
	s, _, _ := strings.Cut(err.Error(), "\n")
	return truncate(s, 200)
}

// Actions implements widget.RawCard.
func (r *rawCard) Actions() <-chan []byte {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	return r.c.actions
}

// flagScope warns about a widget using a scope it didn't declare. Other
// errors pass through untouched.
func (h *runHost) flagScope(err error) {
	var se *widget.ScopeError
	if !errors.As(err, &se) || h.helper {
		return
	}
	what := "used a " + se.Provider + " scope it didn't declare"
	if len(se.Missing) > 0 {
		what += ": " + strings.Join(se.Missing, " or ")
	}
	h.in.warn(what)
}
