package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/tetratelabs/wazero"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
	"github.com/aren55555/widgetd/sdk/go/widget"
)

// Choices: while someone edits an instance's settings, the form asks which
// fields have a fixed set of options. Only the widget knows (which teams a
// league has, which calendars an account has), so the host runs the
// widget's server half briefly, in choices mode, against the settings as
// edited so far: sandboxed as ever, under the instance's network policy,
// with a time limit and no cards.

const (
	choicesTimeout  = 15 * time.Second
	choicesCacheTTL = 5 * time.Minute
	maxChoiceFields = 32
	maxChoices      = 1000
	maxChoiceText   = 128
)

type choiceCache struct {
	mu sync.Mutex
	m  map[string]choiceEntry
}

type choiceEntry struct {
	fields  []*widgetdv1.ConfigFieldChoices
	expires time.Time
}

func (c *choiceCache) get(key string) ([]*widgetdv1.ConfigFieldChoices, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.fields, true
}

func (c *choiceCache) put(key string, fields []*widgetdv1.ConfigFieldChoices) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]choiceEntry{}
	}
	now := time.Now()
	for k, e := range c.m {
		if now.After(e.expires) {
			delete(c.m, k)
		}
	}
	c.m[key] = choiceEntry{fields: fields, expires: now.Add(choicesCacheTTL)}
}

// configChoices asks the widget behind an instance for its settings
// choices, given settings as edited so far.
func (s *Server) configChoices(ctx context.Context, in *instance, cfg []byte) ([]*widgetdv1.ConfigFieldChoices, error) {
	spec := in.w.spec.Config
	if spec == nil || !in.w.hasMode("choices") {
		return nil, nil
	}
	if len(cfg) > maxPayloadBytes {
		return nil, fmt.Errorf("settings are %d bytes, over the limit", len(cfg))
	}
	// Unsaved settings needn't pass their rules yet (that's what the form is
	// for), but they must at least be the widget's Config.
	if err := proto.Unmarshal(cfg, dynamicpb.NewMessage(spec.ProtoReflect().Descriptor())); err != nil {
		return nil, fmt.Errorf("settings don't decode: %w", err)
	}
	// Secret fields aren't in a form's config; the widget may need them.
	cfg, err := s.withStoredSecrets(in, cfg, nil)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(cfg)
	key := in.w.digest + ":" + hex.EncodeToString(sum[:])
	if fields, ok := s.choices.get(key); ok {
		return fields, nil
	}

	// Compile first: the time limit is for the widget, not for a cold compile
	// (seconds on a small board, the first time after a deploy).
	var cm wazero.CompiledModule
	if in.w.spec.Wasm != nil {
		var err error
		if cm, err = in.w.compiled(s.wasm); err != nil {
			return nil, fmt.Errorf("compile: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, choicesTimeout)
	defer cancel()
	h := &runHost{in: in, ctx: ctx, cancel: cancel, cards: map[string]*card{}, helper: true, helperConfig: cfg}
	var m map[string][]widget.Choice
	if cm != nil {
		m, err = s.wasm.choices(ctx, cm, in.w.spec.Name, h)
	} else {
		m, err = in.w.spec.Choices(ctx, h)
	}
	h.close()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("the widget took too long to offer choices")
		}
		return nil, err
	}
	fields := choiceFields(m)
	s.choices.put(key, fields)
	return fields, nil
}

// choiceFields is m, bounded and in a stable order.
func choiceFields(m map[string][]widget.Choice) []*widgetdv1.ConfigFieldChoices {
	var out []*widgetdv1.ConfigFieldChoices
	for field, cs := range m {
		if len(out) >= maxChoiceFields || field == "" || len(field) > maxChoiceText {
			continue
		}
		fc := &widgetdv1.ConfigFieldChoices{Field: field}
		for _, c := range cs {
			if len(fc.Choices) >= maxChoices || !utf8.ValidString(c.Value) || !utf8.ValidString(c.Label) {
				continue
			}
			label := c.Label
			if label == "" {
				label = c.Value
			}
			fc.Choices = append(fc.Choices, &widgetdv1.ConfigChoice{Value: truncate(c.Value, maxChoiceText), Label: truncate(label, maxChoiceText)})
		}
		out = append(out, fc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

func (a *adminService) GetConfigChoices(ctx context.Context, req *connect.Request[widgetdv1.GetConfigChoicesRequest]) (*connect.Response[widgetdv1.GetConfigChoicesResponse], error) {
	in, err := a.targetInstance(a.caller(req), req.Msg.GetTicket(), req.Msg.GetInstanceId())
	if err != nil {
		return nil, err
	}
	fields, err := a.srv.configChoices(ctx, in, req.Msg.GetConfig())
	if err != nil {
		// Choices are a convenience: say why, but the form still works
		// with free text.
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&widgetdv1.GetConfigChoicesResponse{Fields: fields}), nil
}
