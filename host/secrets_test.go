package host

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/protobuf/proto"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	testwidgetv1 "github.com/aren55555/widgetd/internal/testwidget/gen/testwidget/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

func TestSealerBindsValuesToTheirRow(t *testing.T) {
	s, err := openSealer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := s.seal([]byte("hunter2"), "one", "token")
	if bytes.Contains(b, []byte("hunter2")) {
		t.Fatal("sealed value contains the plaintext")
	}
	if v, err := s.open(b, "one", "token"); err != nil || string(v) != "hunter2" {
		t.Fatalf("open = %q, %v", v, err)
	}
	if _, err := s.open(b, "two", "token"); err == nil {
		t.Fatal("a sealed value moved to another instance must not open")
	}
}

func TestSecretFields(t *testing.T) {
	dir := t.TempDir()
	tokens := make(chan string, 4)
	spec := testSpec(func(ctx context.Context, h widget.Host) error {
		var cfg testwidgetv1.Config
		h.Config(&cfg)
		tokens <- cfg.GetToken()
		<-ctx.Done()
		return ctx.Err()
	})
	spec.Secrets = []string{"token"}
	srv, err := New(Options{StateDir: dir, PublicURL: "http://localhost", Widgets: []widget.Spec{spec},
		Instances: []Instance{{ID: "one", Widget: "test", Config: &testwidgetv1.Config{Label: "a", Token: "seeded"}}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	h := &harness{t: t, srv: srv, http: httptest.NewServer(mux)}
	srv.Start()
	t.Cleanup(func() { h.http.Close(); srv.Close(context.Background()) })

	if got := <-tokens; got != "seeded" {
		t.Fatalf("the widget saw token %q, want the seeded one", got)
	}
	noSecretAtRest := func() {
		t.Helper()
		var stored []byte
		srv.db.QueryRow(`SELECT config FROM instances WHERE id = 'one'`).Scan(&stored)
		for _, s := range []string{"seeded", "fresh"} {
			if bytes.Contains(stored, []byte(s)) {
				t.Fatalf("the stored config holds the secret %q", s)
			}
		}
	}
	noSecretAtRest()

	c, admin, ctx := h.adminClient(), h.adminCookie(), context.Background()
	form, err := c.GetConfigForm(ctx, withCookie(&widgetdv1.GetConfigFormRequest{InstanceId: "one"}, admin))
	if err != nil {
		t.Fatal(err)
	}
	var shown testwidgetv1.Config
	proto.Unmarshal(form.Msg.GetConfig(), &shown)
	if shown.GetToken() != "" || shown.GetLabel() != "a" {
		t.Fatalf("the form was sent %v: a secret must never be sent back", &shown)
	}
	if s := form.Msg.GetSecretsSet(); len(s) != 1 || s[0] != "token" || form.Msg.GetSecretFields()[0] != "token" {
		t.Fatalf("secret fields %v, set %v", form.Msg.GetSecretFields(), s)
	}

	save := func(cfg *testwidgetv1.Config, cleared ...string) {
		t.Helper()
		b, _ := proto.Marshal(cfg)
		res, err := c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{InstanceId: "one", Config: b, ClearSecrets: cleared}, admin))
		if err != nil || len(res.Msg.GetViolations()) > 0 {
			t.Fatalf("save: %v %v", res, err)
		}
	}
	// Blank keeps the stored secret.
	save(&testwidgetv1.Config{Label: "b"})
	if got := <-tokens; got != "seeded" {
		t.Fatalf("after a save leaving it blank: %q", got)
	}
	// A new value replaces it, and isn't stored in the clear either.
	save(&testwidgetv1.Config{Label: "b", Token: "fresh"})
	if got := <-tokens; got != "fresh" {
		t.Fatalf("after a new value: %q", got)
	}
	noSecretAtRest()
	// Clearing removes it.
	save(&testwidgetv1.Config{Label: "b"}, "token")
	if got := <-tokens; got != "" {
		t.Fatalf("after clearing: %q", got)
	}
	if _, err := c.SaveConfig(ctx, withCookie(&widgetdv1.SaveConfigRequest{InstanceId: "one", ClearSecrets: []string{"label"}}, admin)); err == nil {
		t.Fatal("clearing a field that isn't secret should be refused")
	}
}

func TestSecretFieldsMustBeTopLevelStrings(t *testing.T) {
	spec := testSpec(nil)
	spec.Run = func(context.Context, widget.Host) error { return nil }
	spec.Secrets = []string{"nope"}
	if _, err := loadWidget(spec); err == nil {
		t.Fatal("a secret naming no field should be refused")
	}
}
