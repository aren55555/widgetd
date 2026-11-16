package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/gen/widgetd/v1/widgetdv1connect"
)

// lockedBuffer is a bytes.Buffer the server's goroutines may log to at once.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// records are the JSON log lines whose source is "view".
func (l *lockedBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(l.b.String()), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if r["source"] == "view" {
			out = append(out, r)
		}
	}
	return out
}

func TestSendLogs(t *testing.T) {
	buf := &lockedBuffer{}
	h := newHarnessOpts(t, func(o *Options) {
		o.Logger = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	h.host() // running
	device := h.deviceCookie("d1")
	c := widgetdv1connect.NewWidgetStreamServiceClient(http.DefaultClient, h.http.URL)
	send := func(cookie string, logs ...*widgetdv1.ViewLog) error {
		_, err := c.SendLogs(context.Background(), withCookie(&widgetdv1.SendLogsRequest{Logs: logs}, cookie))
		return err
	}

	if err := send("", &widgetdv1.ViewLog{CardId: "one/main", Message: "x"}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unpaired: %v", err)
	}

	err := send(device,
		&widgetdv1.ViewLog{
			CardId: "one/main", Level: widgetdv1.ViewLog_LEVEL_ERROR, Message: "boom", Time: timestamppb.New(time.Unix(1_700_000_000, 0)),
			// A view's attrs can't pass for the host's own keys.
			Attrs: map[string]string{"stack": "at x", "instance": "other"},
		},
		// A card that was never mounted (or is gone) still files under its instance...
		&widgetdv1.ViewLog{CardId: "one/gone", Level: widgetdv1.ViewLog_LEVEL_DEBUG, Message: strings.Repeat("é", maxViewLogLine)},
		// ...and one with no instance is dropped.
		&widgetdv1.ViewLog{CardId: "nope/main", Message: "lost"},
		&widgetdv1.ViewLog{CardId: "nope", Message: "lost"},
	)
	if err != nil {
		t.Fatal(err)
	}
	rs := buf.records(t)
	if len(rs) != 2 {
		t.Fatalf("got %d view lines, want 2: %v", len(rs), rs)
	}
	r := rs[0]
	view, _ := r["view"].(map[string]any)
	if r["level"] != "ERROR" || r["msg"] != "boom" || r["instance"] != "one" || r["widget"] != "test" ||
		r["card"] != "one/main" || r["screen"] == "" || r["screen_time"] != "2023-11-14T22:13:20Z" || view["stack"] != "at x" || view["instance"] != "other" {
		t.Errorf("line = %v", r)
	}
	msg, _ := rs[1]["msg"].(string)
	if rs[1]["level"] != "DEBUG" || rs[1]["card"] != "one/gone" || len(msg) > maxViewLogLine+len("…") || !strings.HasSuffix(msg, "é…") {
		t.Errorf("long line = %v (%d bytes)", rs[1]["level"], len(msg))
	}

	// Past the instance's burst, lines are dropped and the drop is logged.
	var many []*widgetdv1.ViewLog
	for range maxViewLogBatch {
		many = append(many, &widgetdv1.ViewLog{CardId: "one/main", Level: widgetdv1.ViewLog_LEVEL_INFO, Message: "spam"})
	}
	for range 2 {
		if err := send(device, many...); err != nil {
			t.Fatal(err)
		}
	}
	var spam, warned int
	for _, r := range buf.records(t) {
		switch r["msg"] {
		case "spam":
			spam++
		case "view logging too fast; lines dropped":
			warned++
		}
	}
	if spam < viewLogBurst-2 || spam > viewLogBurst+20 || warned < 1 {
		t.Errorf("logged %d of %d, warned %d times", spam, 2*maxViewLogBatch, warned)
	}
}

// Every card frame runs the bootstrap (client/src/frame.ts) before the
// widget's code, and its CSP allows exactly that script.
func TestFrameRunsBootFirst(t *testing.T) {
	h := newHarness(t)
	u := h.srv.frameURL(h.srv.widgets["test"])
	if !strings.HasSuffix(u, "?b="+frameBoot.version) {
		t.Fatalf("frame URL %q doesn't name the bootstrap, so cached frames would keep an old one", u)
	}
	resp, err := http.Get(h.http.URL + u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	doc := body.String()
	inline := "<script>" + string(frameBoot.js) + "</script>"
	boot, view := strings.Index(doc, inline), strings.Index(doc, "view.js")
	if boot < 0 || view < 0 || boot > view {
		t.Fatalf("bootstrap at %d, view.js at %d:\n%s", boot, view, doc)
	}
	sum := sha256.Sum256([]byte(frameBoot.js))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src ") || !strings.Contains(csp, want) {
		t.Errorf("CSP %q doesn't allow the bootstrap (%s)", csp, want)
	}
}
