package host

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"golang.org/x/time/rate"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

// Limits on what views may log. Lines beyond them are dropped (or cut
// short), never refused: a view's logging must not break the screen.
const (
	// maxViewLogBatch is how many lines one SendLogs call may carry.
	maxViewLogBatch = 100
	// maxViewLogLine caps a message; maxViewLogAttr caps an attr's key and value.
	maxViewLogLine = 4 << 10
	maxViewLogAttr = 1 << 10
	// maxViewLogAttrs is how many attrs one line keeps.
	maxViewLogAttrs = 16
	// viewLogRate and viewLogBurst limit each instance's view lines, across
	// every screen showing it.
	viewLogRate  = 10
	viewLogBurst = 100
)

var viewLogLevels = map[widgetdv1.ViewLog_Level]slog.Level{
	widgetdv1.ViewLog_LEVEL_DEBUG: slog.LevelDebug,
	widgetdv1.ViewLog_LEVEL_INFO:  slog.LevelInfo,
	widgetdv1.ViewLog_LEVEL_WARN:  slog.LevelWarn,
	widgetdv1.ViewLog_LEVEL_ERROR: slog.LevelError,
}

// SendLogs writes lines that cards' views logged to their instances' logs,
// tagged source=view with the card and the screen they came from. The
// instance's logger already carries its instance and widget.
func (s *streamService) SendLogs(ctx context.Context, req *connect.Request[widgetdv1.SendLogsRequest]) (*connect.Response[widgetdv1.SendLogsResponse], error) {
	who := s.srv.auth.screenOf(req.Header())
	if !who.isScreen() {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("this screen isn't paired"))
	}
	logs := req.Msg.GetLogs()
	dropped := int(req.Msg.GetDropped())
	if len(logs) > maxViewLogBatch {
		dropped += len(logs) - maxViewLogBatch
		logs = logs[:maxViewLogBatch]
	}
	limited := map[*instance]int{}
	for _, l := range logs {
		in := s.srv.instanceOfCard(l.GetCardId())
		if in == nil {
			continue // deleted since: nothing to file it under
		}
		if !in.viewLogs.Allow() {
			limited[in]++
			continue
		}
		level, ok := viewLogLevels[l.GetLevel()]
		if !ok {
			level = slog.LevelInfo
		}
		attrs := []any{
			"source", "view",
			"card", clip(l.GetCardId(), maxViewLogAttr),
			"screen", who.subject,
		}
		if t := l.GetTime(); t.IsValid() {
			attrs = append(attrs, slog.Time("screen_time", t.AsTime()))
		}
		in.log.Log(ctx, level, clip(l.GetMessage(), maxViewLogLine), append(attrs, viewAttrs(l.GetAttrs()))...)
	}
	for in, n := range limited {
		in.log.Warn("view logging too fast; lines dropped", "source", "view", "screen", who.subject, "dropped", n)
	}
	if dropped > 0 {
		s.srv.log.Warn("a screen dropped view log lines", "screen", who.subject, "dropped", dropped)
	}
	return connect.NewResponse(&widgetdv1.SendLogsResponse{}), nil
}

// instanceOfCard is the instance a card belongs to. A card's id is its
// instance's id, "/", then its name; the card itself may have been
// unmounted since the line was logged (a view's last words often are), so
// the instance is found from the id, not the board.
func (s *Server) instanceOfCard(cardID string) *instance {
	id, _, ok := strings.Cut(cardID, "/")
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instances[id]
}

// viewAttrs is a view's attrs as one "view" group, in key order, capped.
func viewAttrs(m map[string]string) slog.Attr {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if len(keys) > maxViewLogAttrs {
		keys = keys[:maxViewLogAttrs]
	}
	attrs := make([]any, 0, len(keys))
	for _, k := range keys {
		attrs = append(attrs, slog.String(clip(k, maxViewLogAttr), clip(m[k], maxViewLogAttr)))
	}
	return slog.Group("view", attrs...)
}

// clip cuts s to at most n bytes without splitting a character, marking
// the cut with an ellipsis.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func newViewLogLimiter() *rate.Limiter {
	return rate.NewLimiter(viewLogRate, viewLogBurst)
}
