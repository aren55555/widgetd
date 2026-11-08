package host

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	"connectrpc.com/connect"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

const keepaliveEvery = 20 * time.Second

type streamService struct {
	srv *Server
}

// StreamWidgets is the one pipe a screen opens. It sends every card's
// Mounted and whatever data the client is missing, then an empty message
// marking the end of that initial burst (a client drops any cached card it
// wasn't told about by then), then live frames and periodic keepalives.
func (s *streamService) StreamWidgets(ctx context.Context, req *connect.Request[widgetdv1.StreamWidgetsRequest], stream *connect.ServerStream[widgetdv1.StreamWidgetsResponse]) error {
	who := s.srv.auth.callerOf(req.Header())
	if !who.canView() {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("this screen isn't paired"))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.srv.auth.trackStream(who.subject, cancel)()

	resume := make(map[string]*widgetdv1.Resume, len(req.Msg.GetResume()))
	for _, r := range req.Msg.GetResume() {
		resume[r.GetCardId()] = r
	}
	initial, sub, unsubscribe := s.srv.board.subscribe(resume)
	defer unsubscribe()

	for _, f := range initial {
		if err := stream.Send(&widgetdv1.StreamWidgetsResponse{Frame: f}); err != nil {
			return err
		}
	}
	if err := stream.Send(&widgetdv1.StreamWidgetsResponse{}); err != nil {
		return err
	}

	keepalive := time.NewTicker(keepaliveEvery)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			if s.srv.auth.callerOf(req.Header()).canView() {
				return nil
			}
			return connect.NewError(connect.CodeUnauthenticated, errors.New("this screen was unpaired"))
		case <-sub.gone:
			return connect.NewError(connect.CodeUnavailable, errors.New("fell behind; reconnect to catch up"))
		case f := <-sub.ch:
			if err := stream.Send(&widgetdv1.StreamWidgetsResponse{Frame: f}); err != nil {
				return err
			}
		case <-keepalive.C:
			if err := stream.Send(&widgetdv1.StreamWidgetsResponse{}); err != nil {
				return err
			}
		}
	}
}

// SendAction delivers something a viewer did on a card to its widget,
// after checking it is the card's declared action type and passes that
// type's rules. Delivery doesn't wait for the widget.
func (s *streamService) SendAction(ctx context.Context, req *connect.Request[widgetdv1.SendActionRequest]) (*connect.Response[widgetdv1.SendActionResponse], error) {
	if !s.srv.auth.callerOf(req.Header()).canView() {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("this screen isn't paired"))
	}
	c, ok := s.srv.board.card(req.Msg.GetCardId())
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such card"))
	}
	if c.actionURL == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this card takes no actions"))
	}
	if req.Msg.GetTypeUrl() != c.actionURL {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("not this card's action type"))
	}
	name := protoreflect.FullName(strings.TrimPrefix(c.actionURL, "type.googleapis.com/"))
	if _, err := c.host.in.w.schema.check(name, req.Msg.GetValue()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if !c.host.deliver(c, req.Msg.GetValue()) {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the widget isn't taking actions right now"))
	}
	return connect.NewResponse(&widgetdv1.SendActionResponse{}), nil
}
