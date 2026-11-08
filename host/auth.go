package host

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"google.golang.org/protobuf/types/known/timestamppb"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

const (
	sessionCookie   = "widgetd_session"
	adminSessionTTL = 30 * 24 * time.Hour
	deviceTTL       = 365 * 24 * time.Hour
	ceremonyTTL     = 5 * time.Minute
	pairingTTL      = 10 * time.Minute
	maxPairings     = 32
	pairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I
)

// caller is who made a request.
type caller struct {
	role    widgetdv1.Role
	subject string // admin id (hex) or device id
}

func (c caller) canView() bool {
	return c.role == widgetdv1.Role_ROLE_ADMIN || c.role == widgetdv1.Role_ROLE_DEVICE
}

// auth implements AuthService and answers "who is this?" for the others.
type auth struct {
	db        *sql.DB
	wa        *webauthn.WebAuthn
	publicURL string
	base      string

	mu         sync.Mutex
	setupToken string // non-empty while no admin exists
	ceremonies map[string]ceremony
	pairings   map[string]*pairing
	streams    map[string]map[*streamKick]struct{} // by subject, for revocation
}

type ceremony struct {
	data    webauthn.SessionData
	adminID []byte // registration only
	expires time.Time
}

type pairing struct {
	secretHash [32]byte
	code       string
	userAgent  string
	expires    time.Time
	approved   bool
	name       string
	changed    chan struct{} // closed and replaced on every state change
}

type streamKick struct{ cancel context.CancelFunc }

func newAuth(db *sql.DB, publicURL, base string) (*auth, error) {
	u, err := parseOrigin(publicURL)
	if err != nil {
		return nil, err
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          u.host,
		RPDisplayName: "widgetd",
		RPOrigins:     []string{u.origin},
	})
	if err != nil {
		return nil, err
	}
	a := &auth{
		db: db, wa: wa, publicURL: u.origin, base: base,
		ceremonies: map[string]ceremony{},
		pairings:   map[string]*pairing{},
		streams:    map[string]map[*streamKick]struct{}{},
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		a.setupToken = randomToken(24)
	}
	return a, nil
}

type origin struct{ origin, host string }

func parseOrigin(s string) (origin, error) {
	s = strings.TrimRight(s, "/")
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || (scheme != "https" && scheme != "http") || rest == "" || strings.ContainsAny(rest, "/?#") {
		return origin{}, fmt.Errorf("public URL %q: want an origin like https://host.example", s)
	}
	host := rest
	if h, _, ok := strings.Cut(rest, ":"); ok {
		host = h
	}
	if scheme == "http" && host != "localhost" {
		return origin{}, fmt.Errorf("public URL %q: passkeys need https (or http://localhost)", s)
	}
	return origin{origin: s, host: host}, nil
}

// setupURL is where the first admin enrols, or "" once one has.
func (a *auth) setupURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.setupToken == "" {
		return ""
	}
	return a.publicURL + a.base + "enroll/" + a.setupToken
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

// ---- sessions ----

func (a *auth) callerOf(h http.Header) caller {
	r := http.Request{Header: h}
	ck, err := r.Cookie(sessionCookie)
	if err != nil || ck.Value == "" {
		return caller{}
	}
	var role int32
	var subject string
	err = a.db.QueryRow(`SELECT role, subject FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		hashToken(ck.Value), time.Now().Unix()).Scan(&role, &subject)
	if err != nil {
		return caller{}
	}
	if widgetdv1.Role(role) == widgetdv1.Role_ROLE_DEVICE {
		a.db.Exec(`UPDATE devices SET last_seen = ? WHERE id = ?`, time.Now().Unix(), subject)
	}
	return caller{role: widgetdv1.Role(role), subject: subject}
}

// startSession stores a new session and returns the cookie that carries it.
func (a *auth) startSession(role widgetdv1.Role, subject string, ttl time.Duration, secure bool) (*http.Cookie, error) {
	token := randomToken(32)
	expires := time.Now().Add(ttl)
	_, err := a.db.Exec(`INSERT INTO sessions (token_hash, role, subject, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), int32(role), subject, expires.Unix())
	if err != nil {
		return nil, err
	}
	return &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	}, nil
}

// secureRequest reports whether the request reached us over TLS, directly or
// through a proxy that terminated it.
func secureRequest(h http.Header, tls bool) bool {
	return tls || strings.EqualFold(h.Get("X-Forwarded-Proto"), "https")
}

func requireAdmin(c caller) error {
	if c.role != widgetdv1.Role_ROLE_ADMIN {
		return connect.NewError(connect.CodePermissionDenied, errors.New("admin sign-in required"))
	}
	return nil
}

// trackStream registers a stream so revoking its subject can end it.
func (a *auth) trackStream(subject string, cancel context.CancelFunc) func() {
	k := &streamKick{cancel: cancel}
	a.mu.Lock()
	if a.streams[subject] == nil {
		a.streams[subject] = map[*streamKick]struct{}{}
	}
	a.streams[subject][k] = struct{}{}
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		delete(a.streams[subject], k)
		if len(a.streams[subject]) == 0 {
			delete(a.streams, subject)
		}
		a.mu.Unlock()
	}
}

func (a *auth) kick(subject string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.streams[subject] {
		k.cancel()
	}
}

// ---- passkeys ----

type adminUser struct {
	id    []byte
	creds []webauthn.Credential
}

func (u *adminUser) WebAuthnID() []byte                         { return u.id }
func (u *adminUser) WebAuthnName() string                       { return "admin" }
func (u *adminUser) WebAuthnDisplayName() string                { return "widgetd admin" }
func (u *adminUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (a *auth) loadAdmin(id []byte) (*adminUser, error) {
	rows, err := a.db.Query(`SELECT data FROM credentials WHERE admin_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	u := &adminUser{id: id}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		u.creds = append(u.creds, c)
	}
	return u, rows.Err()
}

func (a *auth) putCeremony(c ceremony) string {
	id := randomToken(16)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.ceremonies {
		if now.After(v.expires) {
			delete(a.ceremonies, k)
		}
	}
	c.expires = now.Add(ceremonyTTL)
	a.ceremonies[id] = c
	return id
}

func (a *auth) takeCeremony(id string) (ceremony, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.ceremonies[id]
	delete(a.ceremonies, id)
	if !ok || time.Now().After(c.expires) {
		return ceremony{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("sign-in expired; try again"))
	}
	return c, nil
}

// authService is the Connect handler; it needs the raw request for cookies
// and TLS, which Connect exposes through headers only.
type authService struct {
	a *auth
}

func (s *authService) GetSession(ctx context.Context, req *connect.Request[widgetdv1.GetSessionRequest]) (*connect.Response[widgetdv1.GetSessionResponse], error) {
	c := s.a.callerOf(req.Header())
	out := &widgetdv1.GetSessionResponse{Role: c.role, PublicUrl: s.a.publicURL}
	s.a.mu.Lock()
	out.NeedsEnrollment = s.a.setupToken != ""
	s.a.mu.Unlock()
	if c.role == widgetdv1.Role_ROLE_DEVICE {
		s.a.db.QueryRow(`SELECT name FROM devices WHERE id = ?`, c.subject).Scan(&out.DeviceName)
	}
	return connect.NewResponse(out), nil
}

func (s *authService) BeginEnroll(ctx context.Context, req *connect.Request[widgetdv1.BeginEnrollRequest]) (*connect.Response[widgetdv1.BeginEnrollResponse], error) {
	a := s.a
	var user *adminUser
	if c := a.callerOf(req.Header()); c.role == widgetdv1.Role_ROLE_ADMIN {
		id, _ := hex.DecodeString(c.subject)
		u, err := a.loadAdmin(id)
		if err != nil {
			return nil, err
		}
		user = u
	} else {
		a.mu.Lock()
		tok := a.setupToken
		a.mu.Unlock()
		if tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(req.Msg.GetSetupToken())) != 1 {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid or used setup link"))
		}
		id := make([]byte, 32)
		rand.Read(id)
		user = &adminUser{id: id}
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range user.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, data, err := a.wa.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(exclude),
	)
	if err != nil {
		return nil, err
	}
	opts, err := json.Marshal(creation.Response)
	if err != nil {
		return nil, err
	}
	id := a.putCeremony(ceremony{data: *data, adminID: user.id})
	return connect.NewResponse(&widgetdv1.BeginEnrollResponse{CeremonyId: id, OptionsJson: string(opts)}), nil
}

func (s *authService) FinishEnroll(ctx context.Context, req *connect.Request[widgetdv1.FinishEnrollRequest]) (*connect.Response[widgetdv1.FinishEnrollResponse], error) {
	a := s.a
	cer, err := a.takeCeremony(req.Msg.GetCeremonyId())
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes([]byte(req.Msg.GetCredentialJson()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	user, err := a.loadAdmin(cer.adminID)
	if err != nil {
		return nil, err
	}
	cred, err := a.wa.CreateCredential(user, cer.data, parsed)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		name = "passkey"
	}

	// Creating the first admin consumes the setup token, atomically with
	// checking it is still there, so one link can't enrol two admins.
	isNew := len(user.creds) == 0
	if isNew {
		a.mu.Lock()
		if a.setupToken == "" {
			a.mu.Unlock()
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("setup link already used"))
		}
		a.setupToken = ""
		a.mu.Unlock()
		if _, err := a.db.Exec(`INSERT INTO admins (id, created_at) VALUES (?, ?)`, cer.adminID, time.Now().Unix()); err != nil {
			return nil, err
		}
	}
	if _, err := a.db.Exec(`INSERT INTO credentials (id, admin_id, name, data, created_at) VALUES (?, ?, ?, ?, ?)`,
		cred.ID, cer.adminID, name, data, time.Now().Unix()); err != nil {
		return nil, err
	}
	resp := connect.NewResponse(&widgetdv1.FinishEnrollResponse{})
	if isNew {
		ck, err := a.startSession(widgetdv1.Role_ROLE_ADMIN, hex.EncodeToString(cer.adminID), adminSessionTTL, secureRequest(req.Header(), strings.HasPrefix(a.publicURL, "https:")))
		if err != nil {
			return nil, err
		}
		resp.Header().Add("Set-Cookie", ck.String())
	}
	return resp, nil
}

func (s *authService) BeginLogin(ctx context.Context, req *connect.Request[widgetdv1.BeginLoginRequest]) (*connect.Response[widgetdv1.BeginLoginResponse], error) {
	assertion, data, err := s.a.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, err
	}
	opts, err := json.Marshal(assertion.Response)
	if err != nil {
		return nil, err
	}
	id := s.a.putCeremony(ceremony{data: *data})
	return connect.NewResponse(&widgetdv1.BeginLoginResponse{CeremonyId: id, OptionsJson: string(opts)}), nil
}

func (s *authService) FinishLogin(ctx context.Context, req *connect.Request[widgetdv1.FinishLoginRequest]) (*connect.Response[widgetdv1.FinishLoginResponse], error) {
	a := s.a
	cer, err := a.takeCeremony(req.Msg.GetCeremonyId())
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(req.Msg.GetCredentialJson()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var adminID []byte
	cred, err := a.wa.ValidateDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		adminID = userHandle
		return a.loadAdmin(userHandle)
	}, cer.data, parsed)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("passkey not recognised"))
	}
	data, _ := json.Marshal(cred)
	a.db.Exec(`UPDATE credentials SET data = ? WHERE id = ?`, data, cred.ID)
	ck, err := a.startSession(widgetdv1.Role_ROLE_ADMIN, hex.EncodeToString(adminID), adminSessionTTL, secureRequest(req.Header(), strings.HasPrefix(a.publicURL, "https:")))
	if err != nil {
		return nil, err
	}
	resp := connect.NewResponse(&widgetdv1.FinishLoginResponse{})
	resp.Header().Add("Set-Cookie", ck.String())
	return resp, nil
}

// ---- pairing ----

func pairingCode() string {
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = pairingAlphabet[int(b[i])%len(pairingAlphabet)]
	}
	return string(b[:3]) + "-" + string(b[3:])
}

func (a *auth) prunePairingsLocked() {
	now := time.Now()
	for id, p := range a.pairings {
		if now.After(p.expires) {
			close(p.changed)
			delete(a.pairings, id)
		}
	}
}

func (s *authService) BeginPairing(ctx context.Context, req *connect.Request[widgetdv1.BeginPairingRequest]) (*connect.Response[widgetdv1.BeginPairingResponse], error) {
	a := s.a
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prunePairingsLocked()
	if len(a.pairings) >= maxPairings {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many screens waiting to pair; try again shortly"))
	}
	id, secret := randomToken(16), randomToken(32)
	p := &pairing{
		secretHash: sha256.Sum256([]byte(secret)),
		code:       pairingCode(),
		userAgent:  req.Header().Get("User-Agent"),
		expires:    time.Now().Add(pairingTTL),
		changed:    make(chan struct{}),
	}
	a.pairings[id] = p
	slog.Info("widgetd: a screen is waiting to be paired", "code", p.code, "approve_url", a.publicURL+a.base+"pair/"+id)
	return connect.NewResponse(&widgetdv1.BeginPairingResponse{
		PairingId:  id,
		Secret:     secret,
		Code:       p.code,
		ApproveUrl: a.publicURL + a.base + "pair/" + id,
		ExpiresAt:  timestamppb.New(p.expires),
	}), nil
}

// screenPairing returns the pairing if the secret matches.
func (a *auth) screenPairing(id, secret string) (*pairing, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pairings[id]
	sum := sha256.Sum256([]byte(secret))
	if !ok || subtle.ConstantTimeCompare(sum[:], p.secretHash[:]) != 1 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("pairing expired; start again"))
	}
	return p, nil
}

func (a *auth) pairingState(p *pairing) (widgetdv1.PairingState, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case p.approved:
		return widgetdv1.PairingState_PAIRING_STATE_APPROVED, p.changed
	case time.Now().After(p.expires):
		return widgetdv1.PairingState_PAIRING_STATE_EXPIRED, p.changed
	default:
		return widgetdv1.PairingState_PAIRING_STATE_PENDING, p.changed
	}
}

func (s *authService) AwaitPairing(ctx context.Context, req *connect.Request[widgetdv1.AwaitPairingRequest], stream *connect.ServerStream[widgetdv1.AwaitPairingResponse]) error {
	p, err := s.a.screenPairing(req.Msg.GetPairingId(), req.Msg.GetSecret())
	if err != nil {
		return err
	}
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		state, changed := s.a.pairingState(p)
		if err := stream.Send(&widgetdv1.AwaitPairingResponse{State: state}); err != nil {
			return err
		}
		if state != widgetdv1.PairingState_PAIRING_STATE_PENDING {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-keepalive.C:
		case <-time.After(time.Until(p.expires)):
		}
	}
}

func (s *authService) CompletePairing(ctx context.Context, req *connect.Request[widgetdv1.CompletePairingRequest]) (*connect.Response[widgetdv1.CompletePairingResponse], error) {
	a := s.a
	p, err := a.screenPairing(req.Msg.GetPairingId(), req.Msg.GetSecret())
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if !p.approved || time.Now().After(p.expires) {
		a.mu.Unlock()
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("not approved yet"))
	}
	delete(a.pairings, req.Msg.GetPairingId()) // single use
	name := p.name
	a.mu.Unlock()

	deviceID := randomToken(12)
	now := time.Now().Unix()
	if _, err := a.db.Exec(`INSERT INTO devices (id, name, paired_at, last_seen) VALUES (?, ?, ?, ?)`, deviceID, name, now, now); err != nil {
		return nil, err
	}
	// The screen may be on plain http (a dashboard on a LAN or tailnet
	// address), so the device cookie is only Secure when the request was.
	ck, err := a.startSession(widgetdv1.Role_ROLE_DEVICE, deviceID, deviceTTL, secureRequest(req.Header(), false))
	if err != nil {
		return nil, err
	}
	resp := connect.NewResponse(&widgetdv1.CompletePairingResponse{DeviceName: name})
	resp.Header().Add("Set-Cookie", ck.String())
	return resp, nil
}

func (s *authService) GetPairing(ctx context.Context, req *connect.Request[widgetdv1.GetPairingRequest]) (*connect.Response[widgetdv1.GetPairingResponse], error) {
	if err := requireAdmin(s.a.callerOf(req.Header())); err != nil {
		return nil, err
	}
	s.a.mu.Lock()
	defer s.a.mu.Unlock()
	p, ok := s.a.pairings[req.Msg.GetPairingId()]
	if !ok || time.Now().After(p.expires) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("this pairing code has expired"))
	}
	return connect.NewResponse(&widgetdv1.GetPairingResponse{
		Code: p.code, UserAgent: p.userAgent, ExpiresAt: timestamppb.New(p.expires),
	}), nil
}

func (s *authService) ApprovePairing(ctx context.Context, req *connect.Request[widgetdv1.ApprovePairingRequest]) (*connect.Response[widgetdv1.ApprovePairingResponse], error) {
	if err := requireAdmin(s.a.callerOf(req.Header())); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetDeviceName())
	if name == "" || len(name) > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("give the screen a name (up to 64 characters)"))
	}
	a := s.a
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.pairings[req.Msg.GetPairingId()]
	if !ok || time.Now().After(p.expires) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("this pairing code has expired"))
	}
	if p.approved {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("already approved"))
	}
	p.approved, p.name = true, name
	close(p.changed)
	p.changed = make(chan struct{})
	return connect.NewResponse(&widgetdv1.ApprovePairingResponse{}), nil
}

func (s *authService) ListDevices(ctx context.Context, req *connect.Request[widgetdv1.ListDevicesRequest]) (*connect.Response[widgetdv1.ListDevicesResponse], error) {
	if err := requireAdmin(s.a.callerOf(req.Header())); err != nil {
		return nil, err
	}
	rows, err := s.a.db.Query(`SELECT id, name, paired_at, last_seen FROM devices ORDER BY paired_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &widgetdv1.ListDevicesResponse{}
	for rows.Next() {
		var d widgetdv1.Device
		var paired, seen int64
		if err := rows.Scan(&d.Id, &d.Name, &paired, &seen); err != nil {
			return nil, err
		}
		d.PairedAt, d.LastSeen = timestamppb.New(time.Unix(paired, 0)), timestamppb.New(time.Unix(seen, 0))
		out.Devices = append(out.Devices, &d)
	}
	return connect.NewResponse(out), rows.Err()
}

func (s *authService) RevokeDevice(ctx context.Context, req *connect.Request[widgetdv1.RevokeDeviceRequest]) (*connect.Response[widgetdv1.RevokeDeviceResponse], error) {
	if err := requireAdmin(s.a.callerOf(req.Header())); err != nil {
		return nil, err
	}
	id := req.Msg.GetId()
	if _, err := s.a.db.Exec(`DELETE FROM sessions WHERE role = ? AND subject = ?`, int32(widgetdv1.Role_ROLE_DEVICE), id); err != nil {
		return nil, err
	}
	if _, err := s.a.db.Exec(`DELETE FROM devices WHERE id = ?`, id); err != nil {
		return nil, err
	}
	s.a.kick(id)
	return connect.NewResponse(&widgetdv1.RevokeDeviceResponse{}), nil
}
