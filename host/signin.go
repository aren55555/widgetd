package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Sign-in: for widgets that use an account. The host, not the widget, runs
// an OAuth 2.0 device flow (RFC 8628): a screen shows a code, someone
// approves it on the provider's site from their phone, and the host stores
// the token, sealed, per instance. The widget asks for requests to be
// signed (widget.Authorize); the host adds the token only for the
// provider's declared API hosts, so it never enters the sandbox and can't
// be sent anywhere else. A 401 means it lapsed: the instance stops and asks
// to be signed in again.

const (
	providerTimeout  = 15 * time.Second
	maxProviderReply = 64 << 10
)

// checkSignIn refuses a manifest whose sign-in is incomplete, or would send
// the token somewhere the widget can't otherwise reach.
func checkSignIn(spec widget.Spec) error {
	s := spec.SignIn
	if s == nil {
		return nil
	}
	if s.Provider == "" {
		return errors.New("sign_in: provider is required")
	}
	for _, u := range []string{s.DeviceURL, s.TokenURL} {
		if pu, err := url.Parse(u); err != nil || pu.Scheme != "https" || pu.Host == "" {
			return fmt.Errorf("sign_in: %q must be an https URL", u)
		}
	}
	if len(s.APIHosts) == 0 {
		return errors.New("sign_in: api_hosts is required: where may the token go?")
	}
	for _, h := range s.APIHosts {
		if !requested(&loadedWidget{spec: spec}, h) {
			return fmt.Errorf("sign_in: api host %q isn't in the widget's network request", h)
		}
	}
	return nil
}

// ---- stored clients and tokens ----

type signInClient struct {
	id, secret string
}

func (s *Server) loadClient(widget string) (signInClient, bool, error) {
	var id string
	var sealed []byte
	err := s.db.QueryRow(`SELECT client_id, client_secret FROM sign_in_clients WHERE widget = ?`, widget).Scan(&id, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return signInClient{}, false, nil
	}
	if err != nil {
		return signInClient{}, false, err
	}
	c := signInClient{id: id}
	if len(sealed) > 0 {
		b, err := s.sealer.open(sealed, "client:"+widget, "secret")
		if err != nil {
			return signInClient{}, false, err
		}
		c.secret = string(b)
	}
	return c, true, nil
}

func (s *Server) setClient(widget, id, secret string) error {
	var sealed []byte
	if secret != "" {
		sealed = s.sealer.seal([]byte(secret), "client:"+widget, "secret")
	}
	_, err := s.db.Exec(`INSERT INTO sign_in_clients (widget, client_id, client_secret) VALUES (?, ?, ?)
		ON CONFLICT (widget) DO UPDATE SET client_id = excluded.client_id,
			client_secret = COALESCE(excluded.client_secret, sign_in_clients.client_secret)`, widget, id, sealed)
	return err
}

type token struct {
	Access  string    `json:"access"`
	Type    string    `json:"type,omitempty"`
	Refresh string    `json:"refresh,omitempty"`
	Expiry  time.Time `json:"expiry,omitempty"`
}

func (s *Server) loadToken(instance string) (*token, error) {
	var sealed []byte
	err := s.db.QueryRow(`SELECT value FROM tokens WHERE instance = ?`, instance).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := s.sealer.open(sealed, instance, "token")
	if err != nil {
		return nil, err
	}
	var t token
	return &t, json.Unmarshal(b, &t)
}

func (s *Server) storeToken(instance string, t *token) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO tokens (instance, value) VALUES (?, ?)
		ON CONFLICT (instance) DO UPDATE SET value = excluded.value`, instance, s.sealer.seal(b, instance, "token"))
	return err
}

func (s *Server) dropToken(instance string) error {
	_, err := s.db.Exec(`DELETE FROM tokens WHERE instance = ?`, instance)
	return err
}

// signedIn reports whether an instance that needs an account has one.
func (s *Server) signedIn(in *instance) bool {
	if in.w.spec.SignIn == nil {
		return true
	}
	t, err := s.loadToken(in.id)
	return err == nil && t != nil
}

// ---- the provider ----

// providerPost POSTs a form to a provider endpoint and decodes its JSON.
func (s *Server) providerPost(ctx context.Context, endpoint string, form url.Values, into any) error {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	if err := json.Unmarshal(b, into); err != nil {
		return fmt.Errorf("%s: %s: unreadable reply", endpoint, resp.Status)
	}
	return nil
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

type tokenReply struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (r tokenReply) token() *token {
	t := &token{Access: r.AccessToken, Type: r.TokenType, Refresh: r.RefreshToken}
	if r.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return t
}

// signIns tracks the one device flow each instance may have in progress.
type signIns struct {
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

// beginSignIn starts a device flow for an instance: it asks the provider
// for a code, then waits for it to be approved in the background.
func (s *Server) beginSignIn(ctx context.Context, in *instance) (*deviceCode, error) {
	si := in.w.spec.SignIn
	if si == nil {
		return nil, errors.New("this widget doesn't sign in")
	}
	client, ok, err := s.loadClient(in.w.spec.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no %s app is registered for %s yet: add its client ID on the admin page", si.Provider, in.w.title())
	}
	var dc deviceCode
	err = s.providerPost(ctx, si.DeviceURL, url.Values{"client_id": {client.id}, "scope": {strings.Join(si.Scopes, " ")}}, &dc)
	if err != nil {
		return nil, err
	}
	if dc.Error != "" || dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, fmt.Errorf("%s refused: %s", si.Provider, firstNonEmpty(dc.ErrorDescription, dc.Error, "no code"))
	}
	if dc.Interval <= 0 {
		dc.Interval = 5
	}
	if dc.ExpiresIn <= 0 {
		dc.ExpiresIn = 900
	}

	s.signIns.mu.Lock()
	if s.signIns.active == nil {
		s.signIns.active = map[string]context.CancelFunc{}
	}
	if cancel, ok := s.signIns.active[in.id]; ok {
		cancel() // a newer code replaces any still waiting
	}
	pctx, cancel := context.WithTimeout(s.runCtxOrBackground(), time.Duration(dc.ExpiresIn)*time.Second)
	s.signIns.active[in.id] = cancel
	s.signIns.mu.Unlock()

	go s.awaitApproval(pctx, cancel, in, client, dc)
	return &dc, nil
}

func (s *Server) runCtxOrBackground() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCtx != nil {
		return s.runCtx
	}
	return context.Background()
}

// awaitApproval polls the token endpoint until the code is approved,
// refused or expired, as the provider paces it.
func (s *Server) awaitApproval(ctx context.Context, cancel context.CancelFunc, in *instance, client signInClient, dc deviceCode) {
	defer cancel()
	defer func() {
		s.signIns.mu.Lock()
		delete(s.signIns.active, in.id)
		s.signIns.mu.Unlock()
	}()
	si := in.w.spec.SignIn
	interval := time.Duration(dc.Interval) * time.Second
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN, "That sign-in code expired. Try again.")
			}
			return
		case <-time.After(interval):
		}
		form := url.Values{
			"client_id":   {client.id},
			"device_code": {dc.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		if client.secret != "" {
			form.Set("client_secret", client.secret)
		}
		var r tokenReply
		if err := s.providerPost(ctx, si.TokenURL, form, &r); err != nil {
			in.log.Warn("sign-in: token poll failed", "err", err)
			continue
		}
		switch r.Error {
		case "":
			if r.AccessToken == "" {
				continue
			}
			if err := s.storeToken(in.id, r.token()); err != nil {
				in.log.Error("sign-in: storing token failed", "err", err)
				return
			}
			in.log.Info("signed in", "provider", si.Provider)
			s.startIfReady(in.id)
			return
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN, "Sign-in was declined.")
			return
		case "expired_token":
			in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN, "That sign-in code expired. Try again.")
			return
		default:
			in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN, fmt.Sprintf("%s refused: %s", si.Provider, firstNonEmpty(r.ErrorDescription, r.Error)))
			return
		}
	}
}

// startIfReady starts an instance that is configured and (if it needs to
// be) signed in, and otherwise sets the state that says what's missing.
func (s *Server) startIfReady(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok {
		return
	}
	s.startIfReadyLocked(in)
}

func (s *Server) startIfReadyLocked(in *instance) {
	switch {
	case !in.configured:
		in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_CONFIG, "")
	case in.state == widgetdv1.InstanceStatus_STATE_INVALID_CONFIG:
		in.setState(in.state, in.message)
	case !s.signedIn(in):
		in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN, "")
	default:
		in.setState(widgetdv1.InstanceStatus_STATE_RUNNING, "")
		s.startLocked(in)
	}
}

// signOut forgets an instance's account and stops it.
func (s *Server) signOut(id, why string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok {
		return errNoInstance
	}
	if err := s.dropToken(id); err != nil {
		return err
	}
	s.stopLocked(in)
	in.setState(widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN, why)
	return nil
}

// authorization is the Authorization header for a signed request from an
// instance to u, or an error saying why it can't be signed.
func (s *Server) authorization(ctx context.Context, in *instance, u *url.URL) (string, error) {
	si := in.w.spec.SignIn
	if si == nil {
		return "", errors.New("this widget doesn't sign in")
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port == "" {
		port = "443"
	}
	allowed := false
	for _, h := range si.APIHosts {
		if hp, err := parseHostPattern(h); err == nil && hp.matches(host, port) {
			allowed = true
		}
	}
	if !allowed || u.Scheme != "https" {
		return "", &PolicyDenied{Rule: "the account's token only goes to " + strings.Join(si.APIHosts, ", "), URL: u.Redacted()}
	}
	t, err := s.loadToken(in.id)
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", errors.New("not signed in")
	}
	if !t.Expiry.IsZero() && time.Until(t.Expiry) < time.Minute {
		if t, err = s.refresh(ctx, in, t); err != nil {
			return "", err
		}
	}
	typ := t.Type
	if typ == "" || strings.EqualFold(typ, "bearer") {
		typ = "Bearer"
	}
	return typ + " " + t.Access, nil
}

// refresh trades a refresh token for a new access token.
func (s *Server) refresh(ctx context.Context, in *instance, t *token) (*token, error) {
	if t.Refresh == "" {
		go s.signOut(in.id, "The sign-in expired. Sign in again.")
		return nil, errors.New("sign-in expired")
	}
	client, _, err := s.loadClient(in.w.spec.Name)
	if err != nil {
		return nil, err
	}
	form := url.Values{"client_id": {client.id}, "grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}}
	if client.secret != "" {
		form.Set("client_secret", client.secret)
	}
	var r tokenReply
	if err := s.providerPost(ctx, in.w.spec.SignIn.TokenURL, form, &r); err != nil {
		return nil, err
	}
	if r.Error != "" || r.AccessToken == "" {
		go s.signOut(in.id, "The sign-in expired. Sign in again.")
		return nil, errors.New("sign-in expired")
	}
	nt := r.token()
	if nt.Refresh == "" {
		nt.Refresh = t.Refresh
	}
	return nt, s.storeToken(in.id, nt)
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

func (a *adminService) BeginSignIn(ctx context.Context, req *connect.Request[widgetdv1.BeginSignInRequest]) (*connect.Response[widgetdv1.BeginSignInResponse], error) {
	c := a.caller(req)
	if err := requireViewer(c); err != nil {
		return nil, err
	}
	in, err := a.srv.instance(req.Msg.GetInstanceId())
	if err != nil {
		return nil, err
	}
	// A screen may sign in an instance that has no account yet; replacing
	// one is an admin's call.
	if c.role != widgetdv1.Role_ROLE_ADMIN && in.status().GetState() != widgetdv1.InstanceStatus_STATE_NEEDS_SIGN_IN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this widget is already signed in; signing it in again needs an admin"))
	}
	dc, err := a.srv.beginSignIn(ctx, in)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&widgetdv1.BeginSignInResponse{
		UserCode:                dc.UserCode,
		VerificationUri:         dc.VerificationURI,
		VerificationUriComplete: dc.VerificationURIComplete,
		ExpiresAt:               timestamppb.New(time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)),
		Provider:                in.w.spec.SignIn.Provider,
	}), nil
}

func (a *adminService) SignOut(ctx context.Context, req *connect.Request[widgetdv1.SignOutRequest]) (*connect.Response[widgetdv1.SignOutResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	if err := a.srv.signOut(req.Msg.GetInstanceId(), ""); err != nil {
		return nil, notFound(err)
	}
	return connect.NewResponse(&widgetdv1.SignOutResponse{}), nil
}

func (a *adminService) SetSignInClient(ctx context.Context, req *connect.Request[widgetdv1.SetSignInClientRequest]) (*connect.Response[widgetdv1.SetSignInClientResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	w, ok := a.srv.widgets[req.Msg.GetWidget()]
	if !ok || w.spec.SignIn == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no widget by that name signs in"))
	}
	id := strings.TrimSpace(req.Msg.GetClientId())
	if id == "" {
		// Blank keeps the stored ID, so a secret can be added on its own.
		if c, ok, _ := a.srv.loadClient(w.spec.Name); ok {
			id = c.id
		}
	}
	if id == "" || len(id) > 200 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a client ID is required"))
	}
	if err := a.srv.setClient(w.spec.Name, id, strings.TrimSpace(req.Msg.GetClientSecret())); err != nil {
		return nil, err
	}
	return connect.NewResponse(&widgetdv1.SetSignInClientResponse{}), nil
}
