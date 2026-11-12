package host

import (
	"context"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/gen/widgetd/v1/widgetdv1connect"
)

func (h *harness) adminCookie() string {
	h.t.Helper()
	id := []byte("admin-1")
	h.srv.db.Exec(`INSERT OR IGNORE INTO admins (id, created_at) VALUES (?, 0)`, id)
	ck, err := h.srv.auth.startSession(widgetdv1.Role_ROLE_ADMIN, hex.EncodeToString(id), time.Hour, false)
	if err != nil {
		h.t.Fatal(err)
	}
	return ck.Name + "=" + ck.Value
}

func (h *harness) authClient() widgetdv1connect.AuthServiceClient {
	return widgetdv1connect.NewAuthServiceClient(http.DefaultClient, h.http.URL)
}

func withCookie[T any](msg *T, cookie string) *connect.Request[T] {
	r := connect.NewRequest(msg)
	if cookie != "" {
		r.Header().Set("Cookie", cookie)
	}
	return r
}

func TestPairingEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.authClient()
	admin := h.adminCookie()

	begin, err := c.BeginPairing(ctx, connect.NewRequest(&widgetdv1.BeginPairingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	p := begin.Msg
	if !strings.HasPrefix(p.GetApproveUrl(), "http://localhost/widgetd/pair/") || len(p.GetCode()) != 7 {
		t.Fatalf("begin = %v", p)
	}

	// Not approved yet: no token.
	if _, err := c.CompletePairing(ctx, connect.NewRequest(&widgetdv1.CompletePairingRequest{PairingId: p.GetPairingId(), Secret: p.GetSecret()})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("complete before approval: %v", err)
	}

	await, err := c.AwaitPairing(ctx, connect.NewRequest(&widgetdv1.AwaitPairingRequest{PairingId: p.GetPairingId(), Secret: p.GetSecret()}))
	if err != nil {
		t.Fatal(err)
	}
	if !await.Receive() || await.Msg().GetState() != widgetdv1.PairingState_PAIRING_STATE_PENDING {
		t.Fatalf("first await = %v %v", await.Msg(), await.Err())
	}

	// The phone sees the same code the screen shows.
	got, err := c.GetPairing(ctx, withCookie(&widgetdv1.GetPairingRequest{PairingId: p.GetPairingId()}, admin))
	if err != nil || got.Msg.GetCode() != p.GetCode() {
		t.Fatalf("get pairing = %v, %v", got, err)
	}
	if _, err := c.ApprovePairing(ctx, withCookie(&widgetdv1.ApprovePairingRequest{PairingId: p.GetPairingId(), DeviceName: "Kitchen"}, admin)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApprovePairing(ctx, withCookie(&widgetdv1.ApprovePairingRequest{PairingId: p.GetPairingId(), DeviceName: "Kitchen"}, admin)); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("second approval: %v", err)
	}
	if !await.Receive() || await.Msg().GetState() != widgetdv1.PairingState_PAIRING_STATE_APPROVED {
		t.Fatalf("await after approval = %v %v", await.Msg(), await.Err())
	}

	// The wrong secret can't collect the token, even for an approved pairing.
	if _, err := c.CompletePairing(ctx, connect.NewRequest(&widgetdv1.CompletePairingRequest{PairingId: p.GetPairingId(), Secret: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("complete with wrong secret: %v", err)
	}
	done, err := c.CompletePairing(ctx, connect.NewRequest(&widgetdv1.CompletePairingRequest{PairingId: p.GetPairingId(), Secret: p.GetSecret()}))
	if err != nil {
		t.Fatal(err)
	}
	setCookie := done.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "HttpOnly") || done.Msg.GetDeviceName() != "Kitchen" {
		t.Fatalf("complete = %q %v", setCookie, done.Msg)
	}
	device := strings.SplitN(setCookie, ";", 2)[0]

	// Single use.
	if _, err := c.CompletePairing(ctx, connect.NewRequest(&widgetdv1.CompletePairingRequest{PairingId: p.GetPairingId(), Secret: p.GetSecret()})); err == nil {
		t.Fatal("a pairing must not issue a second token")
	}

	// The device can view...
	sess, err := c.GetSession(ctx, withCookie(&widgetdv1.GetSessionRequest{}, device))
	if err != nil || sess.Msg.GetRole() != widgetdv1.Role_ROLE_DEVICE || sess.Msg.GetDeviceName() != "Kitchen" {
		t.Fatalf("session = %v, %v", sess, err)
	}
	h.open(device).burst()

	// ...but not administer.
	if _, err := c.ListDevices(ctx, withCookie(&widgetdv1.ListDevicesRequest{}, device)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("device listing devices: %v", err)
	}
	if _, err := c.ApprovePairing(ctx, withCookie(&widgetdv1.ApprovePairingRequest{PairingId: "x", DeviceName: "y"}, device)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("device approving: %v", err)
	}

	// An admin revokes it, and its open stream ends.
	list, err := c.ListDevices(ctx, withCookie(&widgetdv1.ListDevicesRequest{}, admin))
	if err != nil || len(list.Msg.GetDevices()) != 1 {
		t.Fatalf("devices = %v, %v", list, err)
	}
	f := h.open(device)
	f.burst()
	if _, err := c.RevokeDevice(ctx, withCookie(&widgetdv1.RevokeDeviceRequest{Id: list.Msg.GetDevices()[0].GetId()}, admin)); err != nil {
		t.Fatal(err)
	}
	for f.stream.Receive() {
	}
	if connect.CodeOf(f.stream.Err()) != connect.CodeUnauthenticated {
		t.Fatalf("revoked stream: %v", f.stream.Err())
	}
}

func TestEnrollNeedsSetupToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.authClient()
	sess, _ := c.GetSession(ctx, connect.NewRequest(&widgetdv1.GetSessionRequest{}))
	if !sess.Msg.GetNeedsEnrollment() || h.srv.SetupURL() == "" {
		t.Fatal("a fresh host should need enrolment and offer a setup URL")
	}
	if got := sess.Msg.GetAdminUrl(); got != "http://localhost/widgetd/admin" {
		t.Fatalf("admin_url = %q", got)
	}
	if _, err := c.BeginEnroll(ctx, connect.NewRequest(&widgetdv1.BeginEnrollRequest{SetupToken: "wrong"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("enroll with a wrong token: %v", err)
	}
	token := strings.TrimPrefix(h.srv.SetupURL(), "http://localhost/widgetd/enroll/")
	begin, err := c.BeginEnroll(ctx, connect.NewRequest(&widgetdv1.BeginEnrollRequest{SetupToken: token}))
	if err != nil || !strings.Contains(begin.Msg.GetOptionsJson(), `"residentKey":"required"`) {
		t.Fatalf("begin enroll = %v, %v", begin, err)
	}
}

func TestPublicURLMustBeSecure(t *testing.T) {
	for _, u := range []string{"http://widgets.example.com", "https://x.example.com/path", "widgets.example.com"} {
		if _, err := parseOrigin(u); err == nil {
			t.Errorf("%q should be refused", u)
		}
	}
	for _, u := range []string{"https://x.example.com", "https://x.example.com:8443", "http://localhost:8080"} {
		if _, err := parseOrigin(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
}

// pair runs a whole pairing as approve says, and returns the device cookie
// and what CompletePairing answered.
func (h *harness) pair(approve *widgetdv1.ApprovePairingRequest) (string, *widgetdv1.CompletePairingResponse) {
	h.t.Helper()
	ctx := context.Background()
	c := h.authClient()
	begin, err := c.BeginPairing(ctx, connect.NewRequest(&widgetdv1.BeginPairingRequest{}))
	if err != nil {
		h.t.Fatal(err)
	}
	approve.PairingId = begin.Msg.GetPairingId()
	if _, err := c.ApprovePairing(ctx, withCookie(approve, h.adminCookie())); err != nil {
		h.t.Fatal(err)
	}
	done, err := c.CompletePairing(ctx, connect.NewRequest(&widgetdv1.CompletePairingRequest{PairingId: begin.Msg.GetPairingId(), Secret: begin.Msg.GetSecret()}))
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.SplitN(done.Header().Get("Set-Cookie"), ";", 2)[0], done.Msg
}

func TestContinueScreen(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.authClient()
	admin := h.adminCookie()

	first, _ := h.pair(&widgetdv1.ApprovePairingRequest{DeviceName: "Kitchen"})
	cols := uint32(3)
	saved := &widgetdv1.ScreenState{
		Options: &widgetdv1.BoardOptions{Cols: &cols},
		Cards: []*widgetdv1.CardPrefs{{
			CardId: "clock", View: map[string]string{"face": "analog"}, LocalType: "clock.v1.Local", LocalValue: []byte{1, 2},
		}},
	}
	if _, err := c.SaveScreenState(ctx, withCookie(&widgetdv1.SaveScreenStateRequest{State: saved}, first)); err != nil {
		t.Fatal(err)
	}

	// Only a paired screen saves state, and only state that passes board.proto's rules.
	if _, err := c.SaveScreenState(ctx, withCookie(&widgetdv1.SaveScreenStateRequest{}, admin)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("admin saving screen state: %v", err)
	}
	bad := uint32(99)
	if _, err := c.SaveScreenState(ctx, withCookie(&widgetdv1.SaveScreenStateRequest{State: &widgetdv1.ScreenState{Options: &widgetdv1.BoardOptions{Cols: &bad}}}, first)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("saving cols 99: %v", err)
	}

	list, err := c.ListDevices(ctx, withCookie(&widgetdv1.ListDevicesRequest{}, admin))
	if err != nil {
		t.Fatal(err)
	}
	kitchen := list.Msg.GetDevices()[0]
	if _, err := c.ApprovePairing(ctx, withCookie(&widgetdv1.ApprovePairingRequest{PairingId: "x", ContinueDeviceId: "nope"}, admin)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("continuing a device that doesn't exist: %v", err)
	}

	// A second screen continues Kitchen without the first one's help: same
	// identity and name, the saved settings, and no new device.
	second, done := h.pair(&widgetdv1.ApprovePairingRequest{ContinueDeviceId: kitchen.GetId()})
	if done.GetDeviceName() != "Kitchen" || done.GetScreenState().GetOptions().GetCols() != 3 ||
		done.GetScreenState().GetCards()[0].GetView()["face"] != "analog" {
		t.Fatalf("complete = %v", done)
	}
	list, err = c.ListDevices(ctx, withCookie(&widgetdv1.ListDevicesRequest{}, admin))
	if err != nil || len(list.Msg.GetDevices()) != 1 || list.Msg.GetDevices()[0].GetScreens() != 2 {
		t.Fatalf("devices = %v, %v", list, err)
	}

	// Both keep working...
	f1, f2 := h.open(first), h.open(second)
	f1.burst()
	f2.burst()

	// ...until Kitchen is unpaired, which ends both.
	if _, err := c.RevokeDevice(ctx, withCookie(&widgetdv1.RevokeDeviceRequest{Id: kitchen.GetId()}, admin)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*frames{f1, f2} {
		for f.stream.Receive() {
		}
		if connect.CodeOf(f.stream.Err()) != connect.CodeUnauthenticated {
			t.Fatalf("revoked stream: %v", f.stream.Err())
		}
	}
	var n int
	if err := h.srv.db.QueryRow(`SELECT COUNT(*) FROM screen_state`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("screen_state rows after unpairing = %d, %v", n, err)
	}
}
