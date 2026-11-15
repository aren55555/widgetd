package host

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SnapTrade, as the "snaptrade" sign-in flow: brokerage accounts (e.g.
// Robinhood) through SnapTrade's API keys, which unlike an OAuth app are
// free. It isn't OAuth, so the host does what OAuth would:
//
//   - The client is the developer workspace's Client ID and consumer key,
//     entered on the admin page like any provider's client. Saving them
//     checks them with SnapTrade, and warns if trading is enabled for them.
//   - Each account is a SnapTrade user. Signing in registers one (or reuses
//     the account's), then sends the phone to SnapTrade's Connection Portal
//     with connectionType=read, where the person signs in on the
//     brokerage's own site. Back here, the account is granted "read" only if
//     every one of its connections is read-only; otherwise its SnapTrade
//     user is deleted, with the connections.
//   - The "token" is the user's id and secret. They don't expire.
//   - Signing a request adds clientId, timestamp, userId and userSecret to
//     its query, and an HMAC-SHA256 Signature of {content, path, query}
//     keyed with the consumer key (docs.snaptrade.com/docs/request-signatures).
//   - Signing out deletes the SnapTrade user, which ends its connections.
//
// The host's own calls (register, portal, delete) are its own: a widget
// gets only what its scopes cover, which for SnapTrade is GET.

// snapPending is a SnapTrade sign-in's user, once the phone has opened the
// sign-in link.
type snapPending struct {
	mu     sync.Mutex
	user   string
	secret string
	isNew  bool // registered for this sign-in: deleted if it fails
}

// snapAuthorization is one of a SnapTrade user's brokerage connections.
type snapAuthorization struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // "read" or "trade"
	Disabled  bool   `json:"disabled"`
	Brokerage struct {
		Name string `json:"name"`
	} `json:"brokerage"`
}

// snapUserQuery are a call's credentials for one SnapTrade user.
func snapUserQuery(user, secret string) url.Values {
	return url.Values{"userId": {user}, "userSecret": {secret}}
}

// snapCanonical is v as SnapTrade signs it: JSON with every object's keys
// sorted and no whitespace.
func snapCanonical(v any) ([]byte, error) {
	// Round-trip through generic values: maps marshal with sorted keys.
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// snapSignature is the Signature header for a request: content is its JSON
// body (nil for none), path its URL path, query its raw query as sent.
func snapSignature(consumerKey string, content any, path, query string) (string, error) {
	msg, err := snapCanonical(map[string]any{"content": content, "path": path, "query": query})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(consumerKey))
	mac.Write(msg)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// snapSign signs a widget's request (the host's copy of it) as the
// account's SnapTrade user. Any credentials the widget put in the query
// are replaced.
func snapSign(r *http.Request, c signInClient, t *token) error {
	if t.User == "" {
		return errors.New("the account has no SnapTrade user")
	}
	q := r.URL.Query()
	for _, k := range []string{"clientId", "timestamp", "userId", "userSecret"} {
		q.Del(k)
	}
	q.Set("clientId", c.id)
	q.Set("timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	q.Set("userId", t.User)
	q.Set("userSecret", t.Access)
	r.URL.RawQuery = q.Encode()

	var content any
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close() // read in full; a close error changes nothing
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(b)) > 0 {
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.UseNumber()
			if err := dec.Decode(&content); err != nil {
				return errors.New("a SnapTrade request body must be JSON")
			}
			if m, ok := content.(map[string]any); ok && len(m) == 0 {
				content = nil // SnapTrade signs an empty object as no body
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
		r.ContentLength = int64(len(b))
	}
	sig, err := snapSignature(c.secret, content, r.URL.EscapedPath(), r.URL.RawQuery)
	if err != nil {
		return err
	}
	r.Header.Set("Signature", sig)
	return nil
}

// SnapTradeError is a SnapTrade API call that failed.
type SnapTradeError struct {
	Path   string
	Status string
	Detail string // SnapTrade's own message, if it gave one
}

func (e *SnapTradeError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("SnapTrade %s: %s: %s", e.Path, e.Status, e.Detail)
	}
	return fmt.Sprintf("SnapTrade %s: %s", e.Path, e.Status)
}

// snapCall is one of the host's own signed calls to SnapTrade's API, with
// query (user credentials and parameters) and an optional JSON body,
// decoding a JSON reply into into, if set.
func (s *Server) snapCall(ctx context.Context, p *provider, c signInClient, method, path string, query url.Values, body map[string]any, into any) error {
	u, err := url.Parse(strings.TrimSuffix(p.m.SignIn.APIURL, "/") + path)
	if err != nil {
		return err
	}
	q := url.Values{"clientId": {c.id}, "timestamp": {strconv.FormatInt(time.Now().Unix(), 10)}}
	for k, vs := range query {
		q[k] = vs
	}
	u.RawQuery = q.Encode()
	var content any
	var reqBody io.Reader = http.NoBody
	if len(body) > 0 {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		content, reqBody = body, bytes.NewReader(b)
	}
	sig, err := snapSignature(c.secret, content, u.EscapedPath(), u.RawQuery)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Signature", sig)
	req.Header.Set("Accept", "application/json")
	if content != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Transport: s.transport}).Do(req)
	if err != nil {
		// The URL carries the user's secret: don't let it into an error.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("SnapTrade %s: %w", path, ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderReply))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Detail  string `json:"detail"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &e) // best effort: the status says enough
		return &SnapTradeError{Path: path, Status: resp.Status, Detail: firstNonEmpty(e.Detail, e.Message)}
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(b, into); err != nil {
		return fmt.Errorf("SnapTrade %s: unreadable reply", path)
	}
	return nil
}

// snapRegister registers a new SnapTrade user for an account.
func (s *Server) snapRegister(ctx context.Context, p *provider, c signInClient) (user, secret string, err error) {
	user = "widgetd-" + randomToken(12)
	var reply struct {
		UserID     string `json:"userId"`
		UserSecret string `json:"userSecret"`
	}
	if err := s.snapCall(ctx, p, c, http.MethodPost, "/snapTrade/registerUser", nil, map[string]any{"userId": user}, &reply); err != nil {
		return "", "", err
	}
	if reply.UserSecret == "" {
		return "", "", errors.New("SnapTrade registered no user secret")
	}
	return firstNonEmpty(reply.UserID, user), reply.UserSecret, nil
}

// snapDeleteUser deletes a SnapTrade user and its connections (SnapTrade
// does it asynchronously).
func (s *Server) snapDeleteUser(ctx context.Context, p *provider, c signInClient, user string) error {
	if user == "" {
		return nil
	}
	if c.secret == "" {
		return errors.New("deleting a SnapTrade user needs the consumer key")
	}
	return s.snapCall(ctx, p, c, http.MethodDelete, "/snapTrade/deleteUser", url.Values{"userId": {user}}, nil, nil)
}

// snapDeleteLater deletes a SnapTrade user in the background, best effort.
func (s *Server) snapDeleteLater(p *provider, user, why string) {
	c, _, err := s.loadClient(p.Name)
	if err != nil {
		s.log.Warn("snaptrade: couldn't load the client to delete a user", "err", err)
		return
	}
	s.log.Info("snaptrade: deleting a user", "why", why)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
		defer cancel()
		if err := s.snapDeleteUser(ctx, p, c, user); err != nil {
			s.log.Warn("snaptrade: deleting a user failed", "err", err)
		}
	}()
}

// snapPortal handles a phone opening a SnapTrade sign-in link: it gets the
// account a SnapTrade user, then sends the phone to SnapTrade's Connection
// Portal for a read-only connection.
func (s *Server) snapPortal(w http.ResponseWriter, r *http.Request, state string, ps *pendingSignIn, p *provider, c signInClient) {
	if ps.snap == nil || c.secret == "" {
		oauthPage(w, http.StatusConflict, "Sign-in didn't start", p.Title+"'s API keys aren't set.")
		return
	}
	sp := ps.snap
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.user == "" && ps.account != "" {
		// Signing an account in again: its user, if it still has one.
		if a, err := s.loadAcct(ps.account); err == nil && a != nil && a.Token != nil && a.Token.User != "" {
			sp.user, sp.secret = a.Token.User, a.Token.Access
		}
	}
	if sp.user == "" {
		user, secret, err := s.snapRegister(r.Context(), p, c)
		if err != nil {
			oauthPage(w, http.StatusBadGateway, "Sign-in didn't start", err.Error())
			return
		}
		sp.user, sp.secret, sp.isNew = user, secret, true
	}
	var reply struct {
		RedirectURI string `json:"redirectURI"`
	}
	err := s.snapCall(r.Context(), p, c, http.MethodPost, "/snapTrade/login", snapUserQuery(sp.user, sp.secret), map[string]any{
		"connectionType":    "read",
		"customRedirect":    s.auth.publicURL + s.base + "oauth/snaptrade/" + state,
		"immediateRedirect": true,
	}, &reply)
	if err == nil && !strings.HasPrefix(reply.RedirectURI, "https://") {
		err = errors.New("SnapTrade sent no https Connection Portal link")
	}
	if err != nil {
		oauthPage(w, http.StatusBadGateway, "Sign-in didn't start", err.Error())
		return
	}
	http.Redirect(w, r, reply.RedirectURI, http.StatusFound)
}

// snapReturned handles the Connection Portal sending the phone back: the
// account is signed in, granted "read", only if every one of its
// connections is read-only.
func (s *Server) snapReturned(w http.ResponseWriter, r *http.Request, ps *pendingSignIn) {
	p := s.providers[ps.provider]
	sp := ps.snap
	sp.mu.Lock()
	user, secret, isNew := sp.user, sp.secret, sp.isNew
	sp.mu.Unlock()
	if user == "" {
		oauthPage(w, http.StatusBadRequest, "This sign-in has expired", "Tap Sign in on the screen again.")
		return
	}
	notSignedIn := func(status int, msg string) {
		if isNew {
			s.snapDeleteLater(p, user, "its sign-in didn't finish")
		}
		oauthPage(w, status, "Not signed in", msg)
	}
	if st := r.URL.Query().Get("status"); st != "" && !strings.EqualFold(st, "SUCCESS") {
		notSignedIn(http.StatusOK, fmt.Sprintf("%s said: %s.", p.Title, strings.ToLower(st)))
		return
	}
	c, _, err := s.loadClient(p.Name)
	if err != nil {
		notSignedIn(http.StatusInternalServerError, err.Error())
		return
	}
	var auths []snapAuthorization
	if err := s.snapCall(r.Context(), p, c, http.MethodGet, "/authorizations", snapUserQuery(user, secret), nil, &auths); err != nil {
		notSignedIn(http.StatusBadGateway, err.Error())
		return
	}
	if len(auths) == 0 {
		notSignedIn(http.StatusOK, "No brokerage was connected.")
		return
	}
	for _, a := range auths {
		if a.Type == "read" {
			continue
		}
		// Not read-only: delete the whole user, connections and all, even
		// an existing account's (signing it out deletes its user).
		if isNew {
			s.snapDeleteLater(p, user, "a connection allows trading")
		} else {
			s.accountLapsed(ps.account)
		}
		oauthPage(w, http.StatusOK, "Not signed in", fmt.Sprintf(
			"The %s connection allows trading (type %q). widgetd only accepts read-only connections, so it was removed.",
			firstNonEmpty(a.Brokerage.Name, "brokerage"), a.Type))
		return
	}
	s.finishSignIn(w, r, ps, &token{
		Access: secret, Type: "snaptrade", User: user,
		Requested: ps.scopes, Granted: []string{"read"},
	})
}

// snapCheckClient checks a SnapTrade client's keys with SnapTrade, and says
// on the admin page if they don't work or allow trading.
func (s *Server) snapCheckClient(ctx context.Context, p *provider) {
	c, ok, err := s.loadClient(p.Name)
	if err != nil || !ok || c.secret == "" {
		return
	}
	var info struct {
		CanAccessTrades bool `json:"can_access_trades"`
	}
	switch err := s.snapCall(ctx, p, c, http.MethodGet, "/snapTrade/partners", nil, nil, &info); {
	case err != nil:
		s.setAccountError(p.Name, "SnapTrade didn't accept these keys: "+err.Error())
	case info.CanAccessTrades:
		s.setAccountError(p.Name, "Trading is enabled for this Client ID. widgetd only makes and accepts read-only connections, but you can ask SnapTrade to turn trading off.")
	default:
		s.setAccountError(p.Name, "")
	}
}
