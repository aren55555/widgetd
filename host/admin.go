package host

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

const (
	configTicketTTL = 10 * time.Minute
	maxLabel        = 64
)

// ---- configuration tickets ----

// tickets are the one-time links configuration QR codes carry. A ticket
// names one instance and nothing else, and holding one grants nothing by
// itself: the page it opens still needs an admin's passkey session to save.
// What it adds is that the phone lands on the right instance, and that a
// link photographed off a screen stops working after ten minutes, or once
// used.
type tickets struct {
	mu sync.Mutex
	m  map[string]ticket
}

type ticket struct {
	instanceID string
	expires    time.Time
}

func newTickets() *tickets { return &tickets{m: map[string]ticket{}} }

func (t *tickets) issue(instanceID string) (string, time.Time) {
	tok := randomToken(18)
	exp := time.Now().Add(configTicketTTL)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, v := range t.m {
		if now.After(v.expires) {
			delete(t.m, k)
		}
	}
	t.m[tok] = ticket{instanceID: instanceID, expires: exp}
	return tok, exp
}

func (t *tickets) lookup(tok string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if subtle.ConstantTimeCompare([]byte(k), []byte(tok)) == 1 && time.Now().Before(v.expires) {
			return v.instanceID, true
		}
	}
	return "", false
}

func (t *tickets) consume(tok string) {
	t.mu.Lock()
	delete(t.m, tok)
	t.mu.Unlock()
}

// ---- the service ----

type adminService struct {
	srv *Server
}

func (a *adminService) caller(h interface{ Header() http.Header }) caller {
	return a.srv.auth.callerOf(h.Header())
}

func requireViewer(c caller) error {
	if !c.canView() {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("this screen isn't paired"))
	}
	return nil
}

func (a *adminService) widgetInfo(w *loadedWidget) *widgetdv1.WidgetInfo {
	info := &widgetdv1.WidgetInfo{
		Name: w.spec.Name, Version: w.spec.Version, Title: w.title(),
		RequestedHosts: w.spec.Network.Hosts, RequestedMethods: w.spec.Network.Methods,
		NetworkWhy: w.spec.Network.Why, Configurable: w.spec.Config != nil,
	}
	if si := w.spec.SignIn; si != nil {
		info.SignInProvider = si.Provider
		c, ok, _ := a.srv.loadClient(w.spec.Name)
		info.SignInClientSet, info.SignInSecretSet = ok, ok && c.secret != ""
	}
	if g, ok, _ := a.srv.loadGrant(w.spec.Name); ok {
		info.GrantSet, info.NetworkDisabled, info.GrantedHosts = true, g.disabled, g.hosts
	}
	return info
}

func (a *adminService) ListWidgets(ctx context.Context, req *connect.Request[widgetdv1.ListWidgetsRequest]) (*connect.Response[widgetdv1.ListWidgetsResponse], error) {
	if err := requireViewer(a.caller(req)); err != nil {
		return nil, err
	}
	out := &widgetdv1.ListWidgetsResponse{}
	for _, w := range a.srv.sortedWidgets() {
		out.Widgets = append(out.Widgets, a.widgetInfo(w))
	}
	return connect.NewResponse(out), nil
}

func (a *adminService) AddInstance(ctx context.Context, req *connect.Request[widgetdv1.AddInstanceRequest]) (*connect.Response[widgetdv1.AddInstanceResponse], error) {
	if err := requireViewer(a.caller(req)); err != nil {
		return nil, err
	}
	in, err := a.srv.addInstance(req.Msg.GetWidget())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&widgetdv1.AddInstanceResponse{InstanceId: in.id}), nil
}

func (a *adminService) RequestConfigTicket(ctx context.Context, req *connect.Request[widgetdv1.RequestConfigTicketRequest]) (*connect.Response[widgetdv1.RequestConfigTicketResponse], error) {
	if err := requireViewer(a.caller(req)); err != nil {
		return nil, err
	}
	if _, err := a.srv.instance(req.Msg.GetInstanceId()); err != nil {
		return nil, err
	}
	tok, exp := a.srv.tickets.issue(req.Msg.GetInstanceId())
	return connect.NewResponse(&widgetdv1.RequestConfigTicketResponse{
		Url:       a.srv.auth.publicURL + a.srv.base + "configure/" + tok,
		ExpiresAt: timestamppb.New(exp),
	}), nil
}

// targetInstance resolves the instance a configuration request names, for
// an admin: by ticket if it carries one, else by id.
func (a *adminService) targetInstance(c caller, tok, id string) (*instance, error) {
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	if tok == "" {
		if id == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name an instance"))
		}
		return a.srv.instance(id)
	}
	id, ok := a.srv.tickets.lookup(tok)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("this link has expired; scan the code on the screen again"))
	}
	return a.srv.instance(id)
}

func (a *adminService) GetConfigForm(ctx context.Context, req *connect.Request[widgetdv1.GetConfigFormRequest]) (*connect.Response[widgetdv1.GetConfigFormResponse], error) {
	in, err := a.targetInstance(a.caller(req), req.Msg.GetTicket(), req.Msg.GetInstanceId())
	if err != nil {
		return nil, err
	}
	schema, err := proto.MarshalOptions{Deterministic: true}.Marshal(descriptorSet(in.w.spec.Files))
	if err != nil {
		return nil, err
	}
	in.mu.Lock()
	out := &widgetdv1.GetConfigFormResponse{
		InstanceId: in.id,
		Widget:     a.widgetInfo(in.w),
		Label:      in.label,
		Schema:     schema,
	}
	if in.configured {
		out.Config = in.config
	}
	in.mu.Unlock()
	if c := in.w.spec.Config; c != nil {
		out.ConfigType = string(c.ProtoReflect().Descriptor().FullName())
	}
	out.Status = in.status()
	out.SecretFields = in.w.spec.Secrets
	out.SecretsSet = a.srv.secretsSet(in)
	return connect.NewResponse(out), nil
}

func (a *adminService) SaveConfig(ctx context.Context, req *connect.Request[widgetdv1.SaveConfigRequest]) (*connect.Response[widgetdv1.SaveConfigResponse], error) {
	in, err := a.targetInstance(a.caller(req), req.Msg.GetTicket(), req.Msg.GetInstanceId())
	if err != nil {
		return nil, err
	}
	label, err := cleanLabel(req.Msg.GetLabel())
	if err != nil {
		return nil, err
	}
	cfg := req.Msg.GetConfig()
	if err := validClear(in.w, req.Msg.GetClearSecrets()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if c := in.w.spec.Config; c != nil {
		// Secrets left blank keep what's stored; the checks see them all.
		if cfg, err = a.srv.withStoredSecrets(in, cfg, req.Msg.GetClearSecrets()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if v := in.w.schema.violations(c.ProtoReflect().Descriptor().FullName(), cfg); len(v) > 0 {
			return connect.NewResponse(&widgetdv1.SaveConfigResponse{Violations: v}), nil
		}
		// The rules pass; now the widget checks what they can't.
		v, checked, err := a.srv.verifyConfig(ctx, in, cfg)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		if len(v) > 0 {
			return connect.NewResponse(&widgetdv1.SaveConfigResponse{Violations: v}), nil
		}
		cfg = checked
	} else {
		cfg = nil
	}
	if err := a.srv.configure(in.id, label, cfg); err != nil {
		return nil, err
	}
	if tok := req.Msg.GetTicket(); tok != "" {
		a.srv.tickets.consume(tok)
	}
	return connect.NewResponse(&widgetdv1.SaveConfigResponse{}), nil
}

func cleanLabel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxLabel {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("a name is at most 64 characters"))
	}
	return s, nil
}

func (a *adminService) ListInstances(ctx context.Context, req *connect.Request[widgetdv1.ListInstancesRequest]) (*connect.Response[widgetdv1.ListInstancesResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	out := &widgetdv1.ListInstancesResponse{}
	a.srv.mu.Lock()
	ids := append([]string(nil), a.srv.instanceOrder...)
	a.srv.mu.Unlock()
	for _, id := range ids {
		in, err := a.srv.instance(id)
		if err != nil {
			continue
		}
		in.mu.Lock()
		label := in.label
		in.mu.Unlock()
		out.Instances = append(out.Instances, &widgetdv1.InstanceInfo{
			Id: in.id, Widget: in.w.spec.Name, Label: label, Status: in.status(), CreatedAt: timestamppb.New(in.created),
		})
	}
	return connect.NewResponse(out), nil
}

func (a *adminService) RenameInstance(ctx context.Context, req *connect.Request[widgetdv1.RenameInstanceRequest]) (*connect.Response[widgetdv1.RenameInstanceResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	label, err := cleanLabel(req.Msg.GetLabel())
	if err != nil {
		return nil, err
	}
	if err := a.srv.rename(req.Msg.GetInstanceId(), label); err != nil {
		return nil, notFound(err)
	}
	return connect.NewResponse(&widgetdv1.RenameInstanceResponse{}), nil
}

func (a *adminService) DeleteInstance(ctx context.Context, req *connect.Request[widgetdv1.DeleteInstanceRequest]) (*connect.Response[widgetdv1.DeleteInstanceResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	if err := a.srv.deleteInstance(req.Msg.GetInstanceId()); err != nil {
		return nil, notFound(err)
	}
	return connect.NewResponse(&widgetdv1.DeleteInstanceResponse{}), nil
}

func (a *adminService) SetNetworkGrant(ctx context.Context, req *connect.Request[widgetdv1.SetNetworkGrantRequest]) (*connect.Response[widgetdv1.SetNetworkGrantResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	m := req.Msg
	if err := a.srv.setGrant(m.GetWidget(), m.GetHosts(), m.GetDisabled(), m.GetUseDefault()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&widgetdv1.SetNetworkGrantResponse{}), nil
}

func (a *adminService) GetInstanceActivity(ctx context.Context, req *connect.Request[widgetdv1.GetInstanceActivityRequest]) (*connect.Response[widgetdv1.GetInstanceActivityResponse], error) {
	if err := requireAdmin(a.caller(req)); err != nil {
		return nil, err
	}
	in, err := a.srv.instance(req.Msg.GetInstanceId())
	if err != nil {
		return nil, err
	}
	out := &widgetdv1.GetInstanceActivityResponse{}
	for _, act := range in.fetcher.Activity() {
		out.Fetches = append(out.Fetches, &widgetdv1.Fetch{
			At: timestamppb.New(act.At), Method: act.Method, Host: act.Host, Path: act.Path,
			Status: int32(act.Status), Bytes: act.Bytes, Denied: act.Denied, Error: act.Err,
			DurationMs: act.Duration.Milliseconds(),
		})
	}
	return connect.NewResponse(out), nil
}

func notFound(err error) error {
	if errors.Is(err, errNoInstance) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return err
}

// instance looks one up by id.
func (s *Server) instance(id string) (*instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.instances[id]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errNoInstance)
	}
	return in, nil
}

func (s *Server) sortedWidgets() []*loadedWidget {
	var out []*loadedWidget
	for _, w := range s.widgets {
		out = append(out, w)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].title() < out[j-1].title(); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// violations is check, as field-level messages a form can show inline.
func (s *schema) violations(name protoreflect.FullName, b []byte) []*widgetdv1.FieldViolation {
	_, err := s.check(name, b)
	if err == nil {
		return nil
	}
	var ve *protovalidate.ValidationError
	if errors.As(err, &ve) {
		var out []*widgetdv1.FieldViolation
		for _, v := range ve.Violations {
			out = append(out, &widgetdv1.FieldViolation{
				Field:   protovalidate.FieldPathString(v.Proto.GetField()),
				Message: v.Proto.GetMessage(),
			})
		}
		return out
	}
	return []*widgetdv1.FieldViolation{{Message: err.Error()}}
}

// descriptorSet is files and everything they import, imports first.
func descriptorSet(files []protoreflect.FileDescriptor) *descriptorpb.FileDescriptorSet {
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var walk func(fd protoreflect.FileDescriptor)
	walk = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		imps := fd.Imports()
		for i := range imps.Len() {
			walk(imps.Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
	}
	for _, fd := range files {
		walk(fd)
	}
	return set
}
