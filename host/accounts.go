package host

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Accounts: the display has accounts with providers ("Google: Work",
// "Google: Personal", "GitHub"), each named by an admin when it's first
// signed in. Every instance using a provider is bound, on the admin page, to
// one of its accounts, and each account's grant is kept to exactly the union
// of the scopes the instances bound to it use.
//
// It's a ledger of uses. Adding, configuring, binding or deleting an
// instance changes what its account is wanted for, and reconcile brings the
// account in line:
//
//	wanted ⊆ granted, nothing extra   fine: start whatever was waiting
//	wanted needs more than granted    the instances needing more wait for it
//	                                  to be signed in again (the rest go on)
//	asked for more than wanted        revoke now; its instances wait for a
//	                                  smaller sign-in
//	nothing wanted                    revoke; the account stays, named and
//	                                  signed out
//
// The token never enters a widget: the host signs a widget's requests, and
// only with the account it's bound to, and only what its own scopes cover.
//
// Revoking may end every grant the person has given this client (Google and
// GitHub both work that way), so revoking one token signs out every account
// here that is the same person.

const (
	providerTimeout  = 15 * time.Second
	maxProviderReply = 64 << 10
	signInTTL        = 10 * time.Minute
)

// ---- uses ----

// checkUses refuses a widget using a provider the host doesn't know.
func checkUses(spec widget.Spec, providers map[string]*provider) error {
	for _, u := range spec.Uses {
		p, ok := providers[u.Provider]
		if !ok {
			return fmt.Errorf("uses %q, which this host has no provider for", u.Provider)
		}
		if len(u.Scopes) == 0 {
			return fmt.Errorf("uses %s with no scopes", u.Provider)
		}
		// Strict: only scopes the provider's table lists exist here.
		for _, sc := range u.Scopes {
			if !p.knows(sc) {
				return fmt.Errorf("uses %s scope %q, which the %s provider doesn't list", u.Provider, sc, p.Title)
			}
		}
	}
	return nil
}

func (w *loadedWidget) scopesFor(provider string) []string {
	var out []string
	for _, u := range w.spec.Uses {
		if u.Provider == provider {
			out = append(out, u.Scopes...)
		}
	}
	return out
}

func (w *loadedWidget) usedProviders() []string {
	var out []string
	for _, u := range w.spec.Uses {
		if !slices.Contains(out, u.Provider) {
			out = append(out, u.Provider)
		}
	}
	return out
}

// wantedLocked is the union of the scopes the configured instances bound
// to an account use. Callers hold s.mu.
func (s *Server) wantedLocked(a *account) []string {
	set := map[string]bool{}
	for _, in := range s.instances {
		if in.binding(a.Provider) != a.ID || !in.configured || in.deleting.Load() || in.quarantine != "" {
			continue
		}
		for _, sc := range in.w.scopesFor(a.Provider) {
			set[sc] = true
		}
	}
	return sortedKeys(set)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for sc := range set {
		out = append(out, sc)
	}
	sort.Strings(out)
	return out
}

// askFor is what a sign-in for want requests: want, plus whatever the
// provider needs to say who the account is.
func (p *provider) askFor(want []string) []string {
	if len(want) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, sc := range want {
		set[sc] = true
	}
	for _, sc := range p.m.Identity.Scopes {
		set[sc] = true
	}
	return sortedKeys(set)
}

func covers(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// ---- storage ----

type signInClient struct {
	id, secret string
}

func (s *Server) loadClient(provider string) (signInClient, bool, error) {
	var id string
	var sealed []byte
	err := s.db.QueryRow(`SELECT client_id, client_secret FROM provider_clients WHERE provider = ?`, provider).Scan(&id, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return signInClient{}, false, nil
	}
	if err != nil {
		return signInClient{}, false, err
	}
	c := signInClient{id: id}
	if len(sealed) > 0 {
		b, err := s.sealer.open(sealed, "client:"+provider, "secret")
		if err != nil {
			return signInClient{}, false, err
		}
		c.secret = string(b)
	}
	return c, true, nil
}

// setClient stores a provider's client; an empty id or secret keeps the
// stored one, so either can be updated alone.
func (s *Server) setClient(provider, id, secret string) error {
	var sealed []byte
	if secret != "" {
		sealed = s.sealer.seal([]byte(secret), "client:"+provider, "secret")
	}
	if id == "" {
		c, ok, err := s.loadClient(provider)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("a client ID is required")
		}
		id = c.id
	}
	_, err := s.db.Exec(`INSERT INTO provider_clients (provider, client_id, client_secret) VALUES (?, ?, ?)
		ON CONFLICT (provider) DO UPDATE SET client_id = excluded.client_id,
			client_secret = COALESCE(excluded.client_secret, provider_clients.client_secret)`, provider, id, sealed)
	return err
}

// migrateClients carries clients registered per widget (before accounts
// were per display) over to their provider, once.
func (s *Server) migrateClients() error {
	rows, err := s.db.Query(`SELECT widget, client_id, client_secret FROM sign_in_clients`)
	if err != nil {
		return err
	}
	type old struct {
		widget, id string
		sealed     []byte
	}
	var olds []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.widget, &o.id, &o.sealed); err != nil {
			rows.Close()
			return err
		}
		olds = append(olds, o)
	}
	rows.Close()
	for _, o := range olds {
		w, ok := s.widgets[o.widget]
		if !ok || len(w.usedProviders()) != 1 {
			continue
		}
		p := w.usedProviders()[0]
		if _, have, _ := s.loadClient(p); have {
			continue
		}
		var secret string
		if len(o.sealed) > 0 {
			b, err := s.sealer.open(o.sealed, "client:"+o.widget, "secret")
			if err != nil {
				continue
			}
			secret = string(b)
		}
		if err := s.setClient(p, o.id, secret); err != nil {
			return err
		}
		s.log.Info("accounts: carried a widget's sign-in app over to the display's account", "widget", o.widget, "provider", p)
	}
	return nil
}

type token struct {
	Access  string    `json:"access"`
	Type    string    `json:"type,omitempty"`
	Refresh string    `json:"refresh,omitempty"`
	Expiry  time.Time `json:"expiry,omitempty"`
	// Requested is what this sign-in asked for; Granted, what the provider
	// says it gave (a person can untick a scope on Google's consent page).
	Requested []string `json:"requested,omitempty"`
	Granted   []string `json:"granted,omitempty"`
}

// account is one of the display's accounts with a provider.
type account struct {
	ID, Provider string
	Label        string // "" until an admin names it; unusable till then
	Subject      string // who it is, if the provider says
	Token        *token // nil: signed out
	Created      time.Time
}

func (s *Server) loadAcct(id string) (*account, error) {
	if id == "" {
		return nil, nil
	}
	var a account
	var sealed []byte
	var created int64
	err := s.db.QueryRow(`SELECT id, provider, label, subject, value, created FROM identities WHERE id = ?`, id).
		Scan(&a.ID, &a.Provider, &a.Label, &a.Subject, &sealed, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.Created = time.Unix(created, 0)
	if len(sealed) > 0 {
		b, err := s.sealer.open(sealed, "identity:"+id, "token")
		if err != nil {
			return nil, err
		}
		var t token
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		a.Token = &t
	}
	return &a, nil
}

// listAccts lists a provider's accounts ("": every provider's), by name.
func (s *Server) listAccts(provider string) ([]*account, error) {
	rows, err := s.db.Query(`SELECT id FROM identities WHERE provider = ? OR ? = ''
		ORDER BY provider, label = '', lower(label), created`, provider, provider)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]*account, 0, len(ids))
	for _, id := range ids {
		a, err := s.loadAcct(id)
		if err != nil {
			return nil, err
		}
		if a != nil {
			out = append(out, a)
		}
	}
	return out, nil
}

// storeToken signs an account in (t) or out (nil).
func (s *Server) storeToken(id string, t *token) error {
	var sealed []byte
	if t != nil {
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		sealed = s.sealer.seal(b, "identity:"+id, "token")
	}
	_, err := s.db.Exec(`UPDATE identities SET value = ? WHERE id = ?`, sealed, id)
	return err
}

func (s *Server) createAcct(provider, subject string, t *token) (string, error) {
	id := "acct-" + shortID()
	if _, err := s.db.Exec(`INSERT INTO identities (id, provider, label, subject, created) VALUES (?, ?, '', ?, ?)`,
		id, provider, subject, time.Now().Unix()); err != nil {
		return "", err
	}
	return id, s.storeToken(id, t)
}

// revokeToken signs an account out here at once and revokes its token with
// the provider in the background (best effort: the provider may be down;
// the token is gone from here either way). Revoking may end the person's
// whole grant, so every account here that is the same person is signed
// out too.
func (s *Server) revokeToken(a *account, why string) {
	if err := s.storeToken(a.ID, nil); err != nil {
		s.log.Error("accounts: signing out failed", "account", a.ID, "err", err)
	}
	s.revokeWithProvider(a.Provider, a.Subject, a.Token, why)
}

func (s *Server) revokeWithProvider(provider, subject string, t *token, why string) {
	if t == nil {
		return
	}
	if subject != "" {
		if others, err := s.listAccts(provider); err == nil {
			for _, o := range others {
				if o.Token != nil && strings.EqualFold(o.Subject, subject) {
					s.storeToken(o.ID, nil)
				}
			}
		}
	}
	p := s.providers[provider]
	c, _, _ := s.loadClient(provider)
	s.log.Info("accounts: revoking", "provider", provider, "why", why)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
		defer cancel()
		if err := p.revoke(ctx, s, c, t); err != nil {
			s.log.Warn("accounts: revoking with the provider failed; the token is forgotten here", "provider", provider, "err", err)
		}
	}()
}

// ---- bindings ----

// binding is the account an instance uses a provider with ("": none).
func (in *instance) binding(provider string) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.bindings[provider]
}

// bindLocked binds an instance's use of a provider to an account ("":
// unbinds). Callers hold s.mu, and reconcile after.
func (s *Server) bindLocked(in *instance, provider, id string) error {
	var err error
	if id == "" {
		_, err = s.db.Exec(`DELETE FROM bindings WHERE instance = ? AND provider = ?`, in.id, provider)
	} else {
		_, err = s.db.Exec(`INSERT INTO bindings (instance, provider, account) VALUES (?, ?, ?)
			ON CONFLICT (instance, provider) DO UPDATE SET account = excluded.account`, in.id, provider, id)
	}
	if err != nil {
		return err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.bindings == nil {
		in.bindings = map[string]string{}
	}
	if id == "" {
		delete(in.bindings, provider)
	} else {
		in.bindings[provider] = id
	}
	return nil
}

// loadBindings reads which account each instance uses (before Start).
func (s *Server) loadBindings() error {
	rows, err := s.db.Query(`SELECT instance, provider, account FROM bindings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, p, a string
		if err := rows.Scan(&id, &p, &a); err != nil {
			return err
		}
		if in, ok := s.instances[id]; ok {
			if in.bindings == nil {
				in.bindings = map[string]string{}
			}
			in.bindings[p] = a
		}
	}
	return rows.Err()
}

// ---- reconcile ----

// reconcileLocked brings each of a provider's accounts in line with the
// instances bound to it, and every instance using the provider to running
// or waiting. Callers hold s.mu.
func (s *Server) reconcileLocked(provider string) {
	p := s.providers[provider]
	accts, err := s.listAccts(provider)
	if err != nil {
		s.log.Error("accounts: reading accounts failed", "provider", provider, "err", err)
	}
	for _, a := range accts {
		if a.Label == "" {
			// A new sign-in no one named: let it go once it's stale.
			if time.Since(a.Created) > signInTTL {
				s.revokeToken(a, "never named")
				s.db.Exec(`DELETE FROM identities WHERE id = ?`, a.ID)
				s.unbindAllLocked(a.ID)
			}
			continue
		}
		if a.Token == nil {
			continue
		}
		want := s.wantedLocked(a)
		switch {
		case len(want) == 0:
			s.revokeToken(a, "no widget on the board uses it")
		case !covers(p.askFor(want), a.Token.Requested):
			// Signed in for something no longer bound to it: shrink now.
			s.revokeToken(a, "the board needs fewer scopes")
		}
	}
	for _, id := range s.instanceOrder {
		in := s.instances[id]
		if slices.Contains(in.w.usedProviders(), provider) {
			s.startIfReadyLocked(in)
		}
	}
}

// reconcile is reconcileLocked for callers not holding s.mu.
func (s *Server) reconcile(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconcileLocked(provider)
}

func (s *Server) unbindAllLocked(acctID string) {
	s.db.Exec(`DELETE FROM bindings WHERE account = ?`, acctID)
	for _, in := range s.instances {
		in.mu.Lock()
		for p, a := range in.bindings {
			if a == acctID {
				delete(in.bindings, p)
			}
		}
		in.mu.Unlock()
	}
}

// waitingFor says what an instance is missing to run with its accounts:
// the first provider it uses without a bound, named, signed-in account
// covering it, and that account (if one is bound), or "".
func (s *Server) waitingFor(in *instance) (provider string, a *account) {
	for _, p := range in.w.usedProviders() {
		a, err := s.loadAcct(in.binding(p))
		if err != nil || a == nil {
			return p, nil
		}
		if a.Label == "" || a.Token == nil || !covers(a.Token.Granted, in.w.scopesFor(p)) {
			return p, a
		}
	}
	return "", nil
}

// startIfReady starts an instance that is configured and covered by its
// accounts, and otherwise stops it and says what's missing.
func (s *Server) startIfReady(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in, ok := s.instances[id]; ok {
		s.startIfReadyLocked(in)
	}
}

func (s *Server) startIfReadyLocked(in *instance) {
	switch {
	case !in.configured:
		in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_CONFIG, "")
	case in.state == widgetdv1.InstanceStatus_STATE_INVALID_CONFIG:
		in.setState(in.state, in.message)
	case in.quarantine != "":
		s.stopLocked(in)
		in.setState(widgetdv1.InstanceStatus_STATE_FAULTED, in.quarantine)
	default:
		if p, a := s.waitingFor(in); p != "" {
			s.stopLocked(in)
			in.needsSignIn(p, s.providers[p].Title, a)
			return
		}
		if in.cancel == nil {
			in.setState(widgetdv1.InstanceStatus_STATE_RUNNING, "")
			s.startLocked(in)
		}
	}
}

// ---- the provider, over HTTP ----

// providerForm POSTs (or sends) a form and decodes a JSON reply, if into.
func (s *Server) providerForm(ctx context.Context, method, endpoint string, form url.Values, hdr http.Header, into any) error {
	return s.providerDo(ctx, method, endpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), hdr, into)
}

func (s *Server) providerJSON(ctx context.Context, method, endpoint, body string, hdr http.Header) error {
	return s.providerDo(ctx, method, endpoint, "application/json", strings.NewReader(body), hdr, nil)
}

func (s *Server) providerGet(ctx context.Context, endpoint string, hdr http.Header, into any) error {
	return s.providerDo(ctx, http.MethodGet, endpoint, "", http.NoBody, hdr, into)
}

func (s *Server) providerDo(ctx context.Context, method, endpoint, ctype string, body io.Reader, hdr http.Header, into any) error {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Transport: s.transport}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderReply))
	if err != nil {
		return err
	}
	if into == nil {
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("%s: %s", endpoint, resp.Status)
		}
		return nil
	}
	if err := json.Unmarshal(b, into); err != nil {
		return fmt.Errorf("%s: %s: unreadable reply", endpoint, resp.Status)
	}
	return nil
}

type tokenReply struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// token is the reply as an account, for a sign-in that asked for requested.
func (r tokenReply) token(requested []string) *token {
	t := &token{Access: r.AccessToken, Type: r.TokenType, Refresh: r.RefreshToken, Requested: requested}
	if r.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	// Scopes come back space- (Google) or comma- (GitHub) separated. No
	// scope in the reply means the provider gave what was asked.
	t.Granted = strings.FieldsFunc(r.Scope, func(c rune) bool { return c == ' ' || c == ',' })
	if r.Scope == "" {
		t.Granted = requested
	}
	sort.Strings(t.Granted)
	return t
}

// ---- sign-in ----

type pendingSignIn struct {
	provider string
	account  string // signing an existing account in again, or "" for a new one
	instance string // a new one: the instance to bind it to
	scopes   []string
	verifier string // browser flow: the PKCE verifier
	expires  time.Time
	cancel   context.CancelFunc // device flow: its poller
}

type signIns struct {
	mu      sync.Mutex
	pending map[string]*pendingSignIn // by state (browser) or "device:" key
}

func (si *signIns) put(key string, p *pendingSignIn) {
	si.mu.Lock()
	defer si.mu.Unlock()
	if si.pending == nil {
		si.pending = map[string]*pendingSignIn{}
	}
	now := time.Now()
	for k, v := range si.pending {
		if now.After(v.expires) {
			delete(si.pending, k)
		}
	}
	if old, ok := si.pending[key]; ok && old.cancel != nil {
		old.cancel()
	}
	si.pending[key] = p
}

func (si *signIns) take(key string) (*pendingSignIn, bool) {
	si.mu.Lock()
	defer si.mu.Unlock()
	p, ok := si.pending[key]
	delete(si.pending, key)
	if !ok || time.Now().After(p.expires) {
		return nil, false
	}
	return p, true
}

type signInStart struct {
	url, userCode string
	expires       time.Time
}

// beginSignIn starts signing in an existing account, for exactly what the
// instances bound to it want, or a new one, for what the instance it will
// be bound to wants.
func (s *Server) beginSignIn(ctx context.Context, provider, accountID, instanceID string) (*signInStart, error) {
	p, ok := s.providers[provider]
	if !ok {
		return nil, fmt.Errorf("no provider %q", provider)
	}
	c, ok, err := s.loadClient(provider)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no %s app is registered yet: add its client ID under Accounts on the admin page", p.Title)
	}
	ps := &pendingSignIn{provider: provider, account: accountID, instance: instanceID}
	s.mu.Lock()
	if accountID != "" {
		a, err := s.loadAcct(accountID)
		if err != nil || a == nil || a.Provider != provider {
			s.mu.Unlock()
			return nil, fmt.Errorf("no such %s account", p.Title)
		}
		ps.instance = ""
		ps.scopes = p.askFor(s.wantedLocked(a))
	} else {
		in, ok := s.instances[instanceID]
		if !ok || !slices.Contains(in.w.usedProviders(), provider) {
			s.mu.Unlock()
			return nil, fmt.Errorf("no instance using %s to sign a new account in for", p.Title)
		}
		ps.scopes = p.askFor(in.w.scopesFor(provider))
	}
	s.mu.Unlock()
	if len(ps.scopes) == 0 {
		return nil, fmt.Errorf("no widget is bound to this %s account", p.Title)
	}
	ps.expires = time.Now().Add(signInTTL)
	switch p.Flow {
	case flowBrowser:
		if c.secret == "" {
			return nil, fmt.Errorf("%s needs the client secret too", p.Title)
		}
		state := randomToken(16)
		ps.verifier = randomToken(48)
		s.signIns.put(state, ps)
		return &signInStart{url: s.auth.publicURL + s.base + "oauth/go/" + state, expires: ps.expires}, nil
	case flowDevice:
		return s.beginDevice(ctx, p, c, ps)
	}
	return nil, errors.New("unknown sign-in flow")
}

func (s *Server) redirectURI() string { return s.auth.publicURL + s.base + "oauth/callback" }

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// serveOAuth handles <base>oauth/go/<state> (send the phone to the
// provider's consent page) and <base>oauth/callback (the provider sending
// it back with a code).
func (s *Server) serveOAuth(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case strings.HasPrefix(rest, "go/"):
		state := strings.TrimPrefix(rest, "go/")
		s.signIns.mu.Lock()
		ps, ok := s.signIns.pending[state]
		s.signIns.mu.Unlock()
		if !ok || time.Now().After(ps.expires) {
			oauthPage(w, http.StatusNotFound, "This sign-in link has expired", "Tap Sign in on the screen again for a new one.")
			return
		}
		p := s.providers[ps.provider]
		c, _, _ := s.loadClient(ps.provider)
		q := url.Values{
			"client_id":             {c.id},
			"redirect_uri":          {s.redirectURI()},
			"response_type":         {"code"},
			"scope":                 {strings.Join(ps.scopes, " ")},
			"state":                 {state},
			"code_challenge":        {pkceChallenge(ps.verifier)},
			"code_challenge_method": {"S256"},
		}
		for k, v := range p.AuthParams {
			q[k] = v
		}
		http.Redirect(w, r, p.m.SignIn.AuthURL+"?"+q.Encode(), http.StatusFound)

	case rest == "callback":
		q := r.URL.Query()
		ps, ok := s.signIns.take(q.Get("state"))
		if !ok {
			oauthPage(w, http.StatusBadRequest, "This sign-in has expired", "Tap Sign in on the screen again.")
			return
		}
		p := s.providers[ps.provider]
		if e := q.Get("error"); e != "" {
			if e == "invalid_scope" {
				s.consentRejected(ps, q.Get("error_description"))
			}
			oauthPage(w, http.StatusOK, "Not signed in", fmt.Sprintf("%s said: %s.", p.Title, firstNonEmpty(q.Get("error_description"), e)))
			return
		}
		c, _, _ := s.loadClient(ps.provider)
		var reply tokenReply
		err := s.providerForm(r.Context(), http.MethodPost, p.m.SignIn.TokenURL, url.Values{
			"client_id":     {c.id},
			"client_secret": {c.secret},
			"code":          {q.Get("code")},
			"code_verifier": {ps.verifier},
			"grant_type":    {"authorization_code"},
			"redirect_uri":  {s.redirectURI()},
		}, nil, &reply)
		if err == nil && reply.Error == "invalid_scope" {
			s.consentRejected(ps, reply.ErrorDescription)
		}
		if err == nil && (reply.Error != "" || reply.AccessToken == "") {
			err = fmt.Errorf("%s", firstNonEmpty(reply.ErrorDescription, reply.Error, "no token"))
		}
		if err != nil {
			oauthPage(w, http.StatusBadGateway, "Sign-in didn't finish", err.Error())
			return
		}
		id, err := s.signedIn(r.Context(), ps, reply.token(ps.scopes))
		if err != nil {
			oauthPage(w, http.StatusConflict, "Sign-in didn't finish", err.Error())
			return
		}
		if ps.account == "" {
			// A new account: it can't be used until it's named, which the
			// admin page (where this sign-in started) asks for now.
			http.Redirect(w, r, s.base+"admin?name="+url.QueryEscape(id), http.StatusSeeOther)
			return
		}
		oauthPage(w, http.StatusOK, "Signed in to "+p.Title, "You can close this page. The widgets on your screens will start in a moment.")

	default:
		http.NotFound(w, r)
	}
}

// signedIn stores a sign-in: for an existing account (which must be the
// same person, when the provider says who it is), or as a new, unnamed
// account bound to the instance it was for. It returns the account's id.
//
// An existing account's older token isn't revoked: with Google and GitHub
// that would end the new one too. A sign-in for less than before only
// happens after reconcile has already revoked the old one.
func (s *Server) signedIn(ctx context.Context, ps *pendingSignIn, t *token) (string, error) {
	p := s.providers[ps.provider]
	subject, err := s.identify(ctx, p, t)
	if err != nil {
		s.log.Warn("accounts: couldn't tell who signed in", "provider", p.Name, "err", err)
	}
	if ps.account != "" {
		a, err := s.loadAcct(ps.account)
		if err != nil {
			return "", err
		}
		if a == nil {
			s.revokeWithProvider(p.Name, subject, t, "its account was deleted while signing in")
			return "", errors.New("that account was deleted while signing in")
		}
		if a.Subject != "" && subject != "" && !strings.EqualFold(a.Subject, subject) {
			s.revokeWithProvider(p.Name, subject, t, "signed in as someone else")
			msg := fmt.Sprintf("That was %s, but %q is %s. Sign in as %s.", subject, a.Label, a.Subject, a.Subject)
			s.setAccountError(a.ID, msg)
			s.reconcile(p.Name)
			return "", errors.New(msg)
		}
		if err := s.storeToken(a.ID, t); err != nil {
			return "", err
		}
		if a.Subject == "" && subject != "" {
			s.db.Exec(`UPDATE identities SET subject = ? WHERE id = ?`, subject, a.ID)
		}
		s.setAccountError(a.ID, "")
		s.log.Info("accounts: signed in", "account", a.ID, "provider", p.Name, "scopes", t.Granted)
		s.reconcile(p.Name)
		return a.ID, nil
	}
	if subject != "" {
		others, err := s.listAccts(p.Name)
		if err != nil {
			return "", err
		}
		for _, o := range others {
			if strings.EqualFold(o.Subject, subject) {
				// The same person twice is one grant at the provider; keep
				// one account, and say which. Revoking the new token ends
				// that grant, so the existing account is signed out too.
				s.revokeWithProvider(p.Name, subject, t, "already an account here")
				msg := fmt.Sprintf("%s is already the account %q: bind the widget to it instead, then sign %q in again.", subject, o.Label, o.Label)
				s.setAccountError(p.Name, msg)
				s.reconcile(p.Name)
				return "", errors.New(msg)
			}
		}
	}
	id, err := s.createAcct(p.Name, subject, t)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if in, ok := s.instances[ps.instance]; ok {
		if err := s.bindLocked(in, p.Name, id); err != nil {
			s.log.Error("accounts: binding the new account failed", "account", id, "err", err)
		}
	}
	s.mu.Unlock()
	s.setAccountError(p.Name, "")
	s.log.Info("accounts: new account signed in; waiting for a name", "account", id, "provider", p.Name, "scopes", t.Granted)
	s.reconcile(p.Name)
	return id, nil
}

// identify asks the provider who a token belongs to, if it can say.
func (s *Server) identify(ctx context.Context, p *provider, t *token) (string, error) {
	if p.m.Identity.URL == "" {
		return "", nil
	}
	var reply map[string]any
	hdr := http.Header{"Authorization": {"Bearer " + t.Access}}
	if err := s.providerGet(ctx, p.m.Identity.URL, hdr, &reply); err != nil {
		return "", err
	}
	v, _ := reply[p.m.Identity.Field].(string)
	if v == "" {
		return "", fmt.Errorf("no %q in its reply", p.m.Identity.Field)
	}
	return v, nil
}

var oauthTmpl = template.Must(template.New("oauth").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<style>:root{color-scheme:light dark;font:16px/1.5 system-ui,sans-serif}main{max-width:28rem;margin:3rem auto;padding:0 1.25rem}</style>
</head><body><main><h1>{{.Title}}</h1><p>{{.Body}}</p></main></body></html>`))

func oauthPage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	oauthTmpl.Execute(w, struct{ Title, Body string }{title, body})
}

type deviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

func (s *Server) beginDevice(ctx context.Context, p *provider, c signInClient, ps *pendingSignIn) (*signInStart, error) {
	var dc deviceCode
	err := s.providerForm(ctx, http.MethodPost, p.m.SignIn.DeviceURL, url.Values{"client_id": {c.id}, "scope": {strings.Join(ps.scopes, " ")}}, nil, &dc)
	if err != nil {
		return nil, err
	}
	if dc.Error == "invalid_scope" {
		s.consentRejected(ps, dc.ErrorDescription)
	}
	if dc.Error != "" || dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, fmt.Errorf("%s refused: %s", p.Title, firstNonEmpty(dc.ErrorDescription, dc.Error, "no code"))
	}
	if dc.Interval <= 0 {
		dc.Interval = 5
	}
	if dc.ExpiresIn <= 0 {
		dc.ExpiresIn = 900
	}
	expires := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	pctx, cancel := context.WithDeadline(s.runCtxOrBackground(), expires)
	ps.expires, ps.cancel = expires, cancel
	key := "device:" + ps.account
	if ps.account == "" {
		key = "device:new:" + ps.instance
	}
	s.signIns.put(key, ps)
	go s.awaitDevice(pctx, cancel, p, c, dc, ps)
	u := firstNonEmpty(dc.VerificationURIComplete, dc.VerificationURI)
	return &signInStart{url: u, userCode: dc.UserCode, expires: expires}, nil
}

func (s *Server) runCtxOrBackground() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCtx != nil {
		return s.runCtx
	}
	return context.Background()
}

// awaitDevice polls the token endpoint until the code is approved, refused
// or expired, as the provider paces it.
func (s *Server) awaitDevice(ctx context.Context, cancel context.CancelFunc, p *provider, c signInClient, dc deviceCode, ps *pendingSignIn) {
	defer cancel()
	interval := time.Duration(dc.Interval) * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		form := url.Values{
			"client_id":   {c.id},
			"device_code": {dc.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		if c.secret != "" {
			form.Set("client_secret", c.secret)
		}
		var r tokenReply
		if err := s.providerForm(ctx, http.MethodPost, p.m.SignIn.TokenURL, form, nil, &r); err != nil {
			s.log.Warn("accounts: token poll failed", "provider", p.Name, "err", err)
			continue
		}
		switch r.Error {
		case "":
			if r.AccessToken == "" {
				continue
			}
			if _, err := s.signedIn(ctx, ps, r.token(ps.scopes)); err != nil {
				s.log.Error("accounts: storing sign-in failed", "provider", p.Name, "err", err)
			}
			return
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		default:
			if r.Error == "invalid_scope" {
				s.consentRejected(ps, r.ErrorDescription)
			}
			s.log.Info("accounts: sign-in ended", "provider", p.Name, "reason", firstNonEmpty(r.ErrorDescription, r.Error))
			return
		}
	}
}

// ---- signing widgets' requests ----

// authorization is the Authorization header for a signed request from an
// instance, the provider it's for and the account it's signed with; or why
// it can't be signed. A request is signed only with the account the
// instance is bound to, and only if one of the scopes the widget itself
// declared covers it, by its provider's table: a widget can't use a scope
// another widget sharing the account declared.
func (s *Server) authorization(ctx context.Context, in *instance, method string, u *url.URL) (auth, providerName, acct string, err error) {
	var p *provider
	for _, name := range in.w.usedProviders() {
		pr := s.providers[name]
		if !pr.hosts(u) {
			continue
		}
		if ok, would := pr.covers(in.w.scopesFor(name), method, u); !ok {
			return "", "", "", &widget.ScopeError{Provider: pr.Title, Missing: would}
		}
		p = pr
		break
	}
	if p == nil {
		return "", "", "", &PolicyDenied{Rule: "this widget's accounts don't cover " + u.Host, URL: u.Redacted()}
	}
	a, err := s.loadAcct(in.binding(p.Name))
	if err != nil {
		return "", "", "", err
	}
	if a == nil || a.Label == "" || a.Token == nil || !covers(a.Token.Granted, in.w.scopesFor(p.Name)) {
		return "", "", "", fmt.Errorf("%s isn't signed in for this widget", p.Title)
	}
	t := a.Token
	if !t.Expiry.IsZero() && time.Until(t.Expiry) < time.Minute {
		if t, err = s.refresh(ctx, p, a); err != nil {
			return "", "", "", err
		}
	}
	typ := t.Type
	if typ == "" || strings.EqualFold(typ, "bearer") {
		typ = "Bearer"
	}
	return typ + " " + t.Access, p.Name, a.ID, nil
}

// refresh trades an account's refresh token for a new access token.
func (s *Server) refresh(ctx context.Context, p *provider, a *account) (*token, error) {
	t := a.Token
	c, _, err := s.loadClient(p.Name)
	if err != nil {
		return nil, err
	}
	if t.Refresh == "" {
		go s.accountLapsed(a.ID)
		return nil, errors.New("sign-in expired")
	}
	form := url.Values{"client_id": {c.id}, "grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}}
	if c.secret != "" {
		form.Set("client_secret", c.secret)
	}
	var r tokenReply
	if err := s.providerForm(ctx, http.MethodPost, p.m.SignIn.TokenURL, form, nil, &r); err != nil {
		return nil, err
	}
	if r.Error != "" || r.AccessToken == "" {
		go s.accountLapsed(a.ID)
		return nil, errors.New("sign-in expired")
	}
	nt := r.token(t.Requested)
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	if r.Scope == "" {
		nt.Granted = t.Granted
	}
	return nt, s.storeToken(a.ID, nt)
}

// accountLapsed signs out an account the provider stopped accepting (no
// revoke: it's already dead there) and puts its widgets back to waiting.
func (s *Server) accountLapsed(id string) {
	a, err := s.loadAcct(id)
	if err != nil || a == nil {
		return
	}
	if err := s.storeToken(id, nil); err != nil {
		s.log.Error("accounts: signing out a lapsed account failed", "account", id, "err", err)
	}
	s.log.Warn("accounts: sign-in lapsed", "account", id, "provider", a.Provider)
	s.reconcile(a.Provider)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---- RPCs ----

func (a *adminService) ListAccounts(ctx context.Context, req *connect.Request[widgetdv1.ListAccountsRequest]) (*connect.Response[widgetdv1.ListAccountsResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	s := a.srv
	out := &widgetdv1.ListAccountsResponse{}
	used := map[string]bool{}
	for _, w := range s.widgets {
		for _, p := range w.usedProviders() {
			used[p] = true
		}
	}
	for _, n := range sortedKeys(used) {
		p := s.providers[n]
		info := &widgetdv1.ProviderInfo{Name: n, Title: p.Title, ClientHelp: p.m.SignIn.ClientHelp, Error: s.accountError(n)}
		if c, ok, _ := s.loadClient(n); ok {
			info.ClientSet, info.SecretSet = true, c.secret != ""
		}
		if p.Flow == flowBrowser {
			info.RedirectUri = s.redirectURI()
		}
		out.Providers = append(out.Providers, info)
	}
	accts, err := s.listAccts("")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ac := range accts {
		if _, ok := s.providers[ac.Provider]; !ok {
			continue
		}
		info := &widgetdv1.Account{
			Id: ac.ID, Provider: ac.Provider, Label: ac.Label, Identity: ac.Subject,
			SignedIn: ac.Token != nil, Wanted: s.wantedLocked(ac), Error: s.accountError(ac.ID),
		}
		if ac.Token != nil {
			info.Granted = ac.Token.Granted
		}
		for _, id := range s.instanceOrder {
			if s.instances[id].binding(ac.Provider) == ac.ID {
				info.Instances = append(info.Instances, id)
			}
		}
		switch {
		case ac.Label == "":
			info.Message = "Name this account to use it"
		case len(info.Instances) == 0:
			info.Message = "No widget is bound to it"
		case len(info.Wanted) == 0:
		case !info.SignedIn:
			info.Message = "Signed out"
		case !covers(info.Granted, info.Wanted):
			info.Message = fmt.Sprintf("Needs %d more permission(s)", len(info.Wanted)-len(intersect(info.Granted, info.Wanted)))
		}
		out.Accounts = append(out.Accounts, info)
	}
	return connect.NewResponse(out), nil
}

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		if slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func (a *adminService) BeginSignIn(ctx context.Context, req *connect.Request[widgetdv1.BeginSignInRequest]) (*connect.Response[widgetdv1.BeginSignInResponse], error) {
	c := a.caller(req)
	if err := requireViewer(c); err != nil {
		return nil, err
	}
	s := a.srv
	m := req.Msg
	p, ok := s.providers[m.GetProvider()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such provider"))
	}
	if c.role != widgetdv1.Role_ROLE_ADMIN {
		// A screen may sign an account back in that its widgets are waiting
		// for. A new account, or replacing a sign-in that covers what's
		// bound to it, is an admin's call.
		if m.GetAccountId() == "" {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("adding an account needs an admin: use the admin page"))
		}
		ac, _ := s.loadAcct(m.GetAccountId())
		if ac != nil && ac.Token != nil {
			s.mu.Lock()
			want := s.wantedLocked(ac)
			s.mu.Unlock()
			if covers(ac.Token.Granted, want) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this account is already signed in; signing in again needs an admin"))
			}
		}
	}
	st, err := s.beginSignIn(ctx, m.GetProvider(), m.GetAccountId(), m.GetInstanceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&widgetdv1.BeginSignInResponse{
		Url: st.url, UserCode: st.userCode, ExpiresAt: timestamppb.New(st.expires), ProviderTitle: p.Title,
	}), nil
}

func (a *adminService) SignOut(ctx context.Context, req *connect.Request[widgetdv1.SignOutRequest]) (*connect.Response[widgetdv1.SignOutResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	s := a.srv
	ac, err := s.loadAcct(req.Msg.GetAccountId())
	if err != nil {
		return nil, err
	}
	if ac == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such account"))
	}
	if ac.Token != nil {
		s.revokeToken(ac, "signed out")
	}
	s.reconcile(ac.Provider)
	return connect.NewResponse(&widgetdv1.SignOutResponse{}), nil
}

func (a *adminService) LabelAccount(ctx context.Context, req *connect.Request[widgetdv1.LabelAccountRequest]) (*connect.Response[widgetdv1.LabelAccountResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	s := a.srv
	label := strings.TrimSpace(req.Msg.GetLabel())
	if n := len([]rune(label)); n == 0 || n > 60 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a name of 1 to 60 characters is required"))
	}
	ac, err := s.loadAcct(req.Msg.GetAccountId())
	if err != nil {
		return nil, err
	}
	if ac == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such account"))
	}
	others, err := s.listAccts(ac.Provider)
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		if o.ID != ac.ID && strings.EqualFold(o.Label, label) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("there's already a %s account named %q", s.providers[ac.Provider].Title, o.Label))
		}
	}
	if _, err := s.db.Exec(`UPDATE identities SET label = ? WHERE id = ?`, label, ac.ID); err != nil {
		return nil, err
	}
	s.reconcile(ac.Provider)
	return connect.NewResponse(&widgetdv1.LabelAccountResponse{}), nil
}

func (a *adminService) DeleteAccount(ctx context.Context, req *connect.Request[widgetdv1.DeleteAccountRequest]) (*connect.Response[widgetdv1.DeleteAccountResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	s := a.srv
	ac, err := s.loadAcct(req.Msg.GetAccountId())
	if err != nil {
		return nil, err
	}
	if ac == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such account"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ac.Token != nil {
		s.revokeToken(ac, "deleted")
	}
	if _, err := s.db.Exec(`DELETE FROM identities WHERE id = ?`, ac.ID); err != nil {
		return nil, err
	}
	s.unbindAllLocked(ac.ID)
	s.setAccountError(ac.ID, "")
	s.reconcileLocked(ac.Provider)
	return connect.NewResponse(&widgetdv1.DeleteAccountResponse{}), nil
}

func (a *adminService) BindAccount(ctx context.Context, req *connect.Request[widgetdv1.BindAccountRequest]) (*connect.Response[widgetdv1.BindAccountResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	s := a.srv
	m := req.Msg
	if m.GetAccountId() != "" {
		ac, err := s.loadAcct(m.GetAccountId())
		if err != nil {
			return nil, err
		}
		if ac == nil || ac.Provider != m.GetProvider() {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no such account with that provider"))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[m.GetInstanceId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoInstance)
	}
	if !slices.Contains(in.w.usedProviders(), m.GetProvider()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("that widget doesn't use this provider"))
	}
	if err := s.bindLocked(in, m.GetProvider(), m.GetAccountId()); err != nil {
		return nil, err
	}
	// The old account may now be wanted for less (revoke), the new one for
	// more (sign in again).
	s.reconcileLocked(m.GetProvider())
	return connect.NewResponse(&widgetdv1.BindAccountResponse{}), nil
}

func (a *adminService) SetProviderClient(ctx context.Context, req *connect.Request[widgetdv1.SetProviderClientRequest]) (*connect.Response[widgetdv1.SetProviderClientResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	if _, ok := a.srv.providers[req.Msg.GetProvider()]; !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no such provider"))
	}
	id, secret := strings.TrimSpace(req.Msg.GetClientId()), strings.TrimSpace(req.Msg.GetClientSecret())
	if len(id) > 200 || len(secret) > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("too long"))
	}
	if err := a.srv.setClient(req.Msg.GetProvider(), id, secret); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&widgetdv1.SetProviderClientResponse{}), nil
}

// ---- quarantine (B) ----

// consentRejected handles a sign-in the provider refused over its scopes.
// It stops (quarantines) the instances that asked for the scopes at fault,
// so the rest of the board can sign in without them. The provider may name
// those scopes; if not, they're among the scopes added since the last
// sign-in that worked. If nothing can be pinned down (every scope is new),
// the failure goes on the account for an admin to see, and no one is
// quarantined.
func (s *Server) consentRejected(ps *pendingSignIn, description string) {
	p := s.providers[ps.provider]
	errKey := ps.account
	if errKey == "" {
		errKey = ps.provider
	}
	bad := p.rejectedScopes(description)
	if len(bad) == 0 {
		var prev []string
		if ac, _ := s.loadAcct(ps.account); ac != nil && ac.Token != nil {
			prev = ac.Token.Requested
		}
		asked := 0
		for _, sc := range ps.scopes {
			if slices.Contains(p.m.Identity.Scopes, sc) {
				continue
			}
			asked++
			if !slices.Contains(prev, sc) {
				bad = append(bad, sc)
			}
		}
		if len(bad) == 0 || len(bad) == asked {
			s.setAccountError(errKey, fmt.Sprintf("%s refused the sign-in (%s), and didn't say which scope; no earlier sign-in to compare with.", p.Title, firstNonEmpty(description, "invalid_scope")))
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, id := range s.instanceOrder {
		in := s.instances[id]
		if ps.account != "" && in.binding(ps.provider) != ps.account || ps.account == "" && id != ps.instance {
			continue // not one this sign-in was for
		}
		own := intersect(in.w.scopesFor(ps.provider), bad)
		if len(own) == 0 {
			continue
		}
		reason := fmt.Sprintf("Declares a %s scope %s refused: %s", p.Title, p.Title, strings.Join(own, " "))
		if _, err := s.db.Exec(`INSERT INTO quarantine (instance, reason) VALUES (?, ?)
			ON CONFLICT (instance) DO UPDATE SET reason = excluded.reason`, id, reason); err != nil {
			s.log.Error("accounts: quarantining failed", "instance", id, "err", err)
		}
		in.mu.Lock()
		in.quarantine = reason
		in.mu.Unlock()
		n++
	}
	s.setAccountError(errKey, fmt.Sprintf("%s refused %s; %d widget(s) asking for it were stopped. Sign in again for the rest.", p.Title, strings.Join(bad, " "), n))
	s.reconcileLocked(ps.provider)
}

// loadQuarantine reads which instances are quarantined. Callers hold s.mu
// (or haven't started).
func (s *Server) loadQuarantine() error {
	rows, err := s.db.Query(`SELECT instance, reason FROM quarantine`)
	if err != nil {
		return err
	}
	q := map[string]string{}
	for rows.Next() {
		var id, reason string
		if err := rows.Scan(&id, &reason); err != nil {
			rows.Close()
			return err
		}
		q[id] = reason
	}
	rows.Close()
	for id, reason := range q {
		if in, ok := s.instances[id]; ok {
			in.quarantine = reason
		}
	}
	return rows.Err()
}

// unquarantine clears an instance's quarantine (its settings were saved:
// perhaps a new version of it, or a fix).
func (s *Server) unquarantine(in *instance) {
	in.mu.Lock()
	in.quarantine = ""
	in.mu.Unlock()
	s.db.Exec(`DELETE FROM quarantine WHERE instance = ?`, in.id)
}

func (s *Server) setAccountError(provider, msg string) {
	s.accountErrMu.Lock()
	defer s.accountErrMu.Unlock()
	if s.accountErr == nil {
		s.accountErr = map[string]string{}
	}
	if msg == "" {
		delete(s.accountErr, provider)
	} else {
		s.accountErr[provider] = msg
	}
}

func (s *Server) accountError(provider string) string {
	s.accountErrMu.Lock()
	defer s.accountErrMu.Unlock()
	return s.accountErr[provider]
}
