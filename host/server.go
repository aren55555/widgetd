// Package host runs widgetd widgets and serves them to screens.
//
// Embed it in an existing server with New and Register, or run it on its own
// with cmd/widgetd. Either way it owns one path prefix (BasePath) plus its
// Connect services, and nothing else on the mux.
package host

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/aren55555/widgetd/client"
	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/gen/widgetd/v1/widgetdv1connect"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Options configures a Server.
type Options struct {
	// StateDir holds widgetd.db: instance storage, passkeys, sessions and
	// paired devices.
	StateDir string

	// PublicURL is the HTTPS origin admin pages are reached at, e.g.
	// "https://widgets.example.com". Every QR code points here, whatever
	// origin a screen itself was loaded from, because passkeys only work in
	// a secure context on a stable host name. "http://localhost:<port>" is
	// allowed for development.
	PublicURL string

	// BasePath is the prefix the host's pages and assets are served under.
	// Default "/widgetd/".
	BasePath string

	// Widgets the host can run.
	Widgets []widget.Spec

	// Instances seeds the instances to create on first start. More are added,
	// configured and deleted at runtime, from screens and phones, and kept
	// in widgetd.db.
	Instances []Instance

	// Policies grants each widget, by name, its network access. A widget
	// with no entry is granted exactly what its NetworkRequest asks for;
	// an entry can only narrow that.
	Policies map[string]NetworkPolicy

	Logger *slog.Logger

	// allowAddr lets tests reach loopback servers through the guarded dialer.
	allowAddr func(netip.Addr) bool
	// wasmCacheDir overrides where compiled modules are cached (tests share one).
	wasmCacheDir string
}

// Instance seeds a configured copy of a widget: created on first start if
// no instance with its ID exists, and left alone after that (it may have
// been reconfigured from a phone since).
type Instance struct {
	// ID is stable across restarts; cached client state is keyed by it.
	ID     string
	Widget string
	// Config must be the widget's Config type, or nil for its zero value.
	Config proto.Message
}

// Server is a running widget host.
type Server struct {
	base      string
	log       *slog.Logger
	db        *sql.DB
	auth      *auth
	board     *board
	widgets   map[string]*loadedWidget
	policies  map[string]NetworkPolicy
	transport http.RoundTripper
	wasm      *wasmHost // nil unless a widget is sandboxed
	tickets   *tickets

	mu            sync.Mutex // guards instances, instanceOrder, runCtx and each instance's run
	instances     map[string]*instance
	instanceOrder []string
	runCtx        context.Context

	stop context.CancelFunc
	wg   sync.WaitGroup
}

// New validates the widgets, opens the state database, seeds and loads the
// instances. It starts nothing; call Start.
func New(opts Options) (*Server, error) {
	base := opts.BasePath
	if base == "" {
		base = "/widgetd/"
	}
	if !strings.HasPrefix(base, "/") || !strings.HasSuffix(base, "/") {
		return nil, fmt.Errorf("BasePath %q must start and end with /", base)
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		base:      base,
		log:       log.With("component", "widgetd"),
		board:     newBoard(),
		widgets:   map[string]*loadedWidget{},
		policies:  opts.Policies,
		transport: newGuardedTransport(opts.allowAddr),
		tickets:   newTickets(),
		instances: map[string]*instance{},
	}
	for _, spec := range opts.Widgets {
		w, err := loadWidget(spec)
		if err != nil {
			return nil, err
		}
		if _, dup := s.widgets[spec.Name]; dup {
			return nil, fmt.Errorf("widget %s registered twice", spec.Name)
		}
		s.widgets[spec.Name] = w
	}
	cacheDir := opts.wasmCacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(opts.StateDir, "wasm-cache")
	}
	if err := s.compileWasm(cacheDir); err != nil {
		return nil, err
	}

	db, err := openDB(opts.StateDir)
	if err != nil {
		return nil, fmt.Errorf("state db: %w", err)
	}
	s.db = db
	fail := func(err error) (*Server, error) {
		db.Close()
		return nil, err
	}
	for _, w := range s.widgets {
		if err := s.applyGrant(w); err != nil {
			return fail(fmt.Errorf("widget %s: %w", w.spec.Name, err))
		}
	}
	if err := s.seed(opts.Instances); err != nil {
		return fail(err)
	}
	if err := s.loadInstances(); err != nil {
		return fail(fmt.Errorf("instances: %w", err))
	}
	a, err := newAuth(db, opts.PublicURL, base)
	if err != nil {
		return fail(err)
	}
	s.auth = a
	return s, nil
}

// Start runs every configured instance in the background. If no admin has
// enrolled yet, it logs the one-time setup link.
func (s *Server) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	s.mu.Lock()
	s.runCtx = ctx
	for _, id := range s.instanceOrder {
		in := s.instances[id]
		if in.configured && in.state != widgetdv1.InstanceStatus_STATE_INVALID_CONFIG {
			in.state = widgetdv1.InstanceStatus_STATE_RUNNING
			s.startLocked(in)
		}
		s.board.setInstance(id, in.status())
	}
	s.mu.Unlock()
	if u := s.auth.setupURL(); u != "" {
		s.log.Warn("no admin yet: open this link on your phone to enrol a passkey", "url", u)
	}
}

// SetupURL is the one-time link for enrolling the first admin, or "" once
// one exists.
func (s *Server) SetupURL() string { return s.auth.setupURL() }

// Close stops every instance and closes the database.
func (s *Server) Close(ctx context.Context) error {
	if s.stop != nil {
		s.stop()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return errors.Join(ctx.Err(), s.db.Close())
	case <-time.After(10 * time.Second):
		s.log.Warn("instances did not stop within 10s")
	}
	if s.wasm != nil {
		s.wasm.rt.Close(context.Background())
	}
	return s.db.Close()
}

// Register mounts the host's Connect services and its BasePath on mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.Handle(widgetdv1connect.NewWidgetStreamServiceHandler(&streamService{srv: s}))
	mux.Handle(widgetdv1connect.NewAuthServiceHandler(&authService{a: s.auth}))
	mux.Handle(widgetdv1connect.NewWidgetAdminServiceHandler(&adminService{srv: s}))
	mux.HandleFunc(s.base, s.routes)
}

// serveClient serves the built board and admin bundles.
func (s *Server) serveClient(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(client.Dist, "dist/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}

// compileWasm sets up the sandbox runtime if any widget needs it. Modules
// themselves are compiled when their first instance starts (compiled), not
// here: compiling a Go module takes seconds on a small board, and an
// embedding server shouldn't wait for it before serving.
func (s *Server) compileWasm(cacheDir string) error {
	for _, w := range s.widgets {
		if w.spec.Wasm != nil && s.wasm == nil {
			wh, err := newWasmHost(context.Background(), cacheDir)
			if err != nil {
				return fmt.Errorf("wasm runtime: %w", err)
			}
			s.wasm = wh
		}
	}
	return nil
}
