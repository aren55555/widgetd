package host

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tetratelabs/wazero"
	"google.golang.org/protobuf/proto"

	abiv1 "github.com/aren55555/widgetd/gen/widgetd/abi/v1"
	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

// Verify: before settings are saved, and after they pass the schema's
// rules, the widget gets a say. It runs briefly in verify mode against the
// settings, like choices mode, and either objects (field by field, shown
// beside each) or approves, possibly with a tidied copy to save instead.

// hasMode reports whether the widget implements a helper mode.
func (w *loadedWidget) hasMode(mode string) bool {
	if w.spec.Wasm != nil {
		return slices.Contains(w.spec.Modes, mode)
	}
	switch mode {
	case "choices":
		return w.spec.Choices != nil
	case "verify":
		return w.spec.Verify != nil
	}
	return false
}

// verifyConfig asks the widget about settings that pass the rules. It
// returns objections, or the settings to save (cfg, or the widget's tidied
// copy, which must pass the rules too).
func (s *Server) verifyConfig(ctx context.Context, in *instance, cfg []byte) ([]*widgetdv1.FieldViolation, []byte, error) {
	if !in.w.hasMode("verify") {
		return nil, cfg, nil
	}
	var cm wazero.CompiledModule
	if in.w.spec.Wasm != nil {
		var err error
		if cm, err = in.w.compiled(s.wasm); err != nil {
			return nil, nil, fmt.Errorf("compile: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, choicesTimeout)
	defer cancel()
	h := &runHost{in: in, ctx: ctx, cancel: cancel, cards: map[string]*card{}, helper: true, helperConfig: cfg}
	var verdict *abiv1.Verdict
	var err error
	if cm != nil {
		verdict, err = s.wasm.verify(ctx, cm, in.w.spec.Name, h)
	} else {
		nv, nerr := in.w.spec.Verify(ctx, h)
		err = nerr
		if err == nil {
			verdict = &abiv1.Verdict{}
			for _, x := range nv.Violations {
				verdict.Violations = append(verdict.Violations, &abiv1.Violation{Field: x.Field, Message: x.Message})
			}
			if nv.Config != nil {
				verdict.Config, err = proto.Marshal(nv.Config)
			}
		}
	}
	h.close()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, nil, errors.New("the widget took too long to check these settings")
		}
		return nil, nil, fmt.Errorf("the widget couldn't check these settings: %w", err)
	}
	var out []*widgetdv1.FieldViolation
	for _, v := range verdict.GetViolations() {
		out = append(out, &widgetdv1.FieldViolation{Field: truncate(v.GetField(), maxChoiceText), Message: truncate(v.GetMessage(), 300)})
	}
	if len(out) > 0 {
		return out, nil, nil
	}
	if c := verdict.GetConfig(); len(c) > 0 {
		name := in.w.spec.Config.ProtoReflect().Descriptor().FullName()
		if err := in.w.schema.check(name, c); err != nil {
			return nil, nil, fmt.Errorf("the widget tidied these settings into something its own rules reject: %w", err)
		}
		return nil, c, nil
	}
	return nil, cfg, nil
}
