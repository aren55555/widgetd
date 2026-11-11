package host

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

// Instances live in widgetd.db: added from a screen or seeded by the
// embedder, configured from a phone, renamed and deleted at runtime. This
// file is that registry and each instance's lifecycle; runner.go is what
// runs inside one.

// stopTimeout bounds how long stopping one instance may take.
const stopTimeout = 10 * time.Second

// seed inserts the embedder's instances that don't exist yet. An existing
// row wins: once someone has reconfigured a seeded instance from a phone,
// a restart must not put the seed's configuration back.
func (s *Server) seed(seeds []Instance) error {
	seen := map[string]bool{}
	for _, in := range seeds {
		if seen[in.ID] {
			return fmt.Errorf("instance %s defined twice", in.ID)
		}
		seen[in.ID] = true
		if !nameRE.MatchString(in.ID) {
			return fmt.Errorf("instance id %q: want lowercase letters, digits and dashes", in.ID)
		}
		w, ok := s.widgets[in.Widget]
		if !ok {
			return fmt.Errorf("instance %s: no widget %q", in.ID, in.Widget)
		}
		cfg, err := seedConfig(w, in)
		if err != nil {
			return err
		}
		stored, secrets, err := w.splitSecrets(cfg)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO instances (id, widget, config, configured, created_at) VALUES (?, ?, ?, 1, ?)
			ON CONFLICT (id) DO NOTHING`, in.ID, in.Widget, stored, time.Now().UnixNano())
		if err == nil {
			if n, _ := res.RowsAffected(); n == 1 {
				err = s.storeSecrets(tx, in.ID, secrets, nil)
			}
		}
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return err
		}
	}
	return nil
}

func seedConfig(w *loadedWidget, in Instance) ([]byte, error) {
	spec := w.spec.Config
	if spec == nil {
		if in.Config != nil {
			return nil, fmt.Errorf("instance %s: widget %s takes no config", in.ID, in.Widget)
		}
		return nil, nil
	}
	want := spec.ProtoReflect().Descriptor().FullName()
	msg := in.Config
	if msg == nil {
		msg = spec.ProtoReflect().New().Interface()
	}
	if got := msg.ProtoReflect().Descriptor().FullName(); got != want {
		return nil, fmt.Errorf("instance %s: config is %s, want %s", in.ID, got, want)
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if err := w.schema.check(want, b); err != nil {
		return nil, fmt.Errorf("instance %s: %w", in.ID, err)
	}
	return b, nil
}

// loadInstances builds every stored instance whose widget is installed. An
// instance of a widget that isn't (any more) stays in the table, untouched,
// in case the widget comes back.
func (s *Server) loadInstances() error {
	type row struct {
		id, widget, label string
		cfg               []byte
		configured        bool
		created           int64
	}
	// Read every row before loading any secrets: the pool has one
	// connection, and these rows hold it until they're closed.
	rows, err := s.db.Query(`SELECT id, widget, label, config, configured, created_at FROM instances ORDER BY created_at, id`)
	if err != nil {
		return err
	}
	all, err := collect(rows, func(rows *sql.Rows) (row, error) {
		var r row
		err := rows.Scan(&r.id, &r.widget, &r.label, &r.cfg, &r.configured, &r.created)
		return r, err
	})
	if err != nil {
		return err
	}
	for _, r := range all {
		id, widget, label, cfg, configured, created := r.id, r.widget, r.label, r.cfg, r.configured, r.created
		w, ok := s.widgets[widget]
		if !ok {
			s.log.Warn("instance of a widget that isn't installed; skipping", "instance", id, "widget", widget)
			continue
		}
		in := s.newInstance(id, w, label, time.Unix(0, created))
		in.config, in.configured = cfg, configured
		in.state = widgetdv1.InstanceStatus_STATE_NEEDS_CONFIG
		if configured {
			secrets, err := s.loadSecrets(id)
			if err == nil {
				in.runConfig, err = w.mergeSecrets(cfg, secrets)
			}
			if err == nil {
				err = in.checkConfig(in.runConfig)
			}
			if err != nil {
				in.state, in.message = widgetdv1.InstanceStatus_STATE_INVALID_CONFIG, err.Error()
				s.log.Warn("stored configuration no longer valid; instance waits to be reconfigured", "instance", id, "err", err)
			}
		}
		s.instances[id] = in
		s.instanceOrder = append(s.instanceOrder, id)
	}
	return nil
}

func (s *Server) newInstance(id string, w *loadedWidget, label string, created time.Time) *instance {
	return &instance{
		id: id, w: w, srv: s, label: label, created: created,
		log:     s.log.With("instance", id),
		fetcher: newFetcher(w.policy.Load(), s.transport),
	}
}

// checkConfig checks configuration bytes against the widget's Config.
func (in *instance) checkConfig(cfg []byte) error {
	spec := in.w.spec.Config
	if spec == nil {
		if len(cfg) > 0 {
			return errors.New("widget takes no configuration")
		}
		return nil
	}
	err := in.w.schema.check(spec.ProtoReflect().Descriptor().FullName(), cfg)
	return err
}

// status is the instance as screens see it.
func (in *instance) status() *widgetdv1.InstanceStatus {
	in.mu.Lock()
	defer in.mu.Unlock()
	title := in.label
	if title == "" {
		title = in.w.title()
	}
	st := &widgetdv1.InstanceStatus{State: in.state, Widget: in.w.spec.Name, Title: title, Message: in.message, Warning: in.warning, Icon: iconProto(in.w.spec.Icon)}
	if in.state == widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN {
		st.Provider, st.Account, st.AccountLabel = in.waitProvider, in.waitAccount, in.waitLabel
	}
	return st
}

func (in *instance) setState(st widgetdv1.InstanceStatus_State, msg string) {
	in.mu.Lock()
	in.state, in.message = st, msg
	in.mu.Unlock()
	in.srv.board.setInstance(in.id, in.status())
}

// start runs a configured instance in the background. Callers hold s.mu.
func (s *Server) startLocked(in *instance) {
	if s.runCtx == nil || in.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.runCtx)
	done := make(chan struct{})
	in.cancel, in.done = cancel, done
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		in.run(ctx)
	}()
}

// stop ends an instance's run and waits for it, its cards unmounted.
// Callers hold s.mu.
func (s *Server) stopLocked(in *instance) {
	if in.cancel == nil {
		return
	}
	in.cancel()
	select {
	case <-in.done:
	case <-time.After(stopTimeout):
		in.log.Warn("instance did not stop in time")
	}
	in.cancel, in.done = nil, nil
}

// addInstance stores a new, unconfigured instance of a widget.
func (s *Server) addInstance(widget string) (*instance, error) {
	w, ok := s.widgets[widget]
	if !ok {
		return nil, fmt.Errorf("no widget %q", widget)
	}
	id := widget + "-" + shortID()
	if len(id) > 63 {
		id = id[len(id)-63:]
	}
	created := time.Now()
	if _, err := s.db.Exec(`INSERT INTO instances (id, widget, created_at) VALUES (?, ?, ?)`, id, widget, created.UnixNano()); err != nil {
		return nil, err
	}
	in := s.newInstance(id, w, "", created)
	in.state = widgetdv1.InstanceStatus_STATE_NEEDS_CONFIG
	s.mu.Lock()
	s.instances[id] = in
	s.instanceOrder = append(s.instanceOrder, id)
	s.mu.Unlock()
	s.board.setInstance(id, in.status())
	return in, nil
}

// configure checks and stores an instance's configuration (and label), then
// restarts it with it.
func (s *Server) configure(id, label string, cfg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok {
		return errNoInstance
	}
	if err := in.checkConfig(cfg); err != nil {
		return err
	}
	// cfg is whole, secrets and all: store it without them, and them sealed
	// apart. Every secret not in cfg is gone.
	stored, secrets, err := in.w.splitSecrets(cfg)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE instances SET label = ?, config = ?, configured = 1 WHERE id = ?`, label, stored, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM secrets WHERE instance = ?`, id); err != nil {
		return err
	}
	if err := s.storeSecrets(tx, id, secrets, nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.stopLocked(in)
	in.mu.Lock()
	in.label, in.config, in.runConfig, in.configured = label, stored, cfg, true
	in.warning = ""
	in.state, in.message = widgetdv1.InstanceStatus_STATE_UNSPECIFIED, ""
	in.mu.Unlock()
	s.unquarantine(in)
	s.startIfReadyLocked(in) // or wait to be signed in
	return nil
}

func (s *Server) rename(id, label string) error {
	s.mu.Lock()
	in, ok := s.instances[id]
	s.mu.Unlock()
	if !ok {
		return errNoInstance
	}
	if _, err := s.db.Exec(`UPDATE instances SET label = ? WHERE id = ?`, label, id); err != nil {
		return err
	}
	in.mu.Lock()
	in.label = label
	in.mu.Unlock()
	s.board.setInstance(id, in.status())
	return nil
}

// deleteInstance stops an instance and removes it and everything it kept:
// its storage here, and (through Unmounted.purge) what screens cached.
func (s *Server) deleteInstance(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok {
		return errNoInstance
	}
	in.deleting.Store(true)
	s.stopLocked(in)
	s.board.unmountInstance(id, true) // in case it wasn't running
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM kv WHERE instance = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM secrets WHERE instance = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM quarantine WHERE instance = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM bindings WHERE instance = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM instances WHERE id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(s.instances, id)
	for i, o := range s.instanceOrder {
		if o == id {
			s.instanceOrder = append(s.instanceOrder[:i], s.instanceOrder[i+1:]...)
			break
		}
	}
	s.board.removeInstance(id)
	// Its accounts may now be granted more than the board uses: trim them.
	for _, p := range in.w.usedProviders() {
		s.reconcileLocked(p)
	}
	return nil
}

var errNoInstance = errors.New("no such instance")

func shortID() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// ---- network grants ----

type storedGrant struct {
	disabled bool
	hosts    []string // nil: everything requested
}

func (s *Server) loadGrant(widget string) (storedGrant, bool, error) {
	var disabled bool
	var hosts sql.NullString
	err := s.db.QueryRow(`SELECT disabled, hosts FROM grants WHERE widget = ?`, widget).Scan(&disabled, &hosts)
	if errors.Is(err, sql.ErrNoRows) {
		return storedGrant{}, false, nil
	}
	if err != nil {
		return storedGrant{}, false, err
	}
	g := storedGrant{disabled: disabled}
	if hosts.Valid {
		if err := json.Unmarshal([]byte(hosts.String), &g.hosts); err != nil {
			return storedGrant{}, false, err
		}
		if g.hosts == nil {
			g.hosts = []string{}
		}
	}
	return g, true, nil
}

// grantPolicy is the embedder's policy for a widget with any stored grant
// laid over it.
func (s *Server) grantPolicy(w *loadedWidget) (NetworkPolicy, error) {
	p := s.policies[w.spec.Name]
	g, ok, err := s.loadGrant(w.spec.Name)
	if err != nil || !ok {
		return p, err
	}
	p.Disabled = p.Disabled || g.disabled
	if g.hosts != nil {
		p.Hosts = g.hosts
	}
	return p, nil
}

// applyGrant rebuilds a widget's effective policy and hands it to every
// instance's fetcher; the next request is checked against it.
func (s *Server) applyGrant(w *loadedWidget) error {
	p, err := s.grantPolicy(w)
	if err != nil {
		return err
	}
	eff, err := newEffectivePolicy(w.spec.Network, p)
	if err != nil {
		return err
	}
	w.policy.Store(eff)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, in := range s.instances {
		if in.w == w {
			in.fetcher.setPolicy(eff)
		}
	}
	return nil
}

func (s *Server) setGrant(widget string, hosts []string, disabled, reset bool) error {
	w, ok := s.widgets[widget]
	if !ok {
		return fmt.Errorf("no widget %q", widget)
	}
	if reset {
		if _, err := s.db.Exec(`DELETE FROM grants WHERE widget = ?`, widget); err != nil {
			return err
		}
		return s.applyGrant(w)
	}
	for _, h := range hosts {
		if !requested(w, h) {
			return fmt.Errorf("%s isn't a host the widget asked for", h)
		}
	}
	if hosts == nil {
		hosts = []string{}
	}
	b, _ := json.Marshal(hosts)
	_, err := s.db.Exec(`INSERT INTO grants (widget, disabled, hosts) VALUES (?, ?, ?)
		ON CONFLICT (widget) DO UPDATE SET disabled = excluded.disabled, hosts = excluded.hosts`, widget, disabled, string(b))
	if err != nil {
		return err
	}
	return s.applyGrant(w)
}

func requested(w *loadedWidget, host string) bool {
	for _, h := range w.spec.Network.Hosts {
		if strings.EqualFold(strings.TrimSpace(h), strings.TrimSpace(host)) {
			return true
		}
	}
	return false
}

// needsSignIn puts an instance to waiting for an account with a provider:
// one to be bound (a nil a), named, or signed in to cover it.
func (in *instance) needsSignIn(provider, title string, a *account) {
	msg := "Needs a " + title + " account: choose one for it on the admin page"
	in.mu.Lock()
	in.waitProvider, in.waitAccount, in.waitLabel = provider, "", ""
	if a != nil {
		in.waitAccount, in.waitLabel = a.ID, a.Label
		msg = "Needs " + title + " access (" + a.Label + ")"
		if a.Label == "" {
			msg = "Waiting for its new " + title + " account to be named on the admin page"
		}
	}
	in.mu.Unlock()
	in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN, msg)
}

// warn records something wrong with an instance that doesn't stop it, and
// tells screens.
func (in *instance) warn(msg string) {
	in.mu.Lock()
	changed := in.warning != msg
	in.warning = msg
	in.mu.Unlock()
	if changed {
		in.log.Warn("widget instance warning", "warning", msg)
		in.srv.board.setInstance(in.id, in.status())
	}
}
