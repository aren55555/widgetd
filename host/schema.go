package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"

	"buf.build/go/protovalidate"
	"github.com/tetratelabs/wazero"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/aren55555/widgetd/sdk/go/widget"
)

// maxPayloadBytes caps one serialised snapshot, delta or config.
const maxPayloadBytes = 1 << 20

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// schema is one widget's types, resolved only from the files the widget
// declared. A payload is checked against it by decoding it as a dynamic
// message, not as any Go type the host might happen to have linked in, so
// the same check works for a widget whose code the host has never seen.
type schema struct {
	files     *protoregistry.Files
	validator protovalidate.Validator
}

func newSchema(fds []protoreflect.FileDescriptor) (*schema, error) {
	files := new(protoregistry.Files)
	for _, fd := range fds {
		if err := files.RegisterFile(fd); err != nil {
			return nil, err
		}
	}
	v, err := protovalidate.New()
	if err != nil {
		return nil, err
	}
	return &schema{files: files, validator: v}, nil
}

func (s *schema) message(name protoreflect.FullName) (protoreflect.MessageDescriptor, error) {
	d, err := s.files.FindDescriptorByName(name)
	if err != nil {
		return nil, fmt.Errorf("type %s is not in the widget's schema", name)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s is not a message", name)
	}
	return md, nil
}

// check decodes b as message `name` and enforces the schema's validation
// rules. Bytes that don't decode, carry fields the schema doesn't define, or
// break a rule are refused.
func (s *schema) check(name protoreflect.FullName, b []byte) (proto.Message, error) {
	if len(b) > maxPayloadBytes {
		return nil, fmt.Errorf("%s is %d bytes, over the %d limit", name, len(b), maxPayloadBytes)
	}
	md, err := s.message(name)
	if err != nil {
		return nil, err
	}
	msg := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(b, msg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	if hasUnknown(msg) {
		return nil, fmt.Errorf("%s carries fields its schema doesn't define", name)
	}
	if err := s.validator.Validate(msg); err != nil {
		return nil, fmt.Errorf("validate %s: %w", name, err)
	}
	return msg, nil
}

// hasUnknown reports whether m, or any message inside it, has unknown fields.
func hasUnknown(m protoreflect.Message) bool {
	if len(m.GetUnknown()) > 0 {
		return true
	}
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			l := v.List()
			for i := 0; i < l.Len() && !found; i++ {
				found = hasUnknown(l.Get(i).Message())
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				found = hasUnknown(mv.Message())
				return !found
			})
		case fd.Message() != nil && !fd.IsList() && !fd.IsMap():
			found = hasUnknown(v.Message())
		}
		return !found
	})
	return found
}

func typeURL(name protoreflect.FullName) string {
	if name == "" {
		return ""
	}
	return "type.googleapis.com/" + string(name)
}

// loadedWidget is a widget the host can run: its spec plus what the host
// derives from it once.
type loadedWidget struct {
	spec   widget.Spec
	schema *schema
	digest string
	policy atomic.Pointer[effectivePolicy]
	// For sandboxed widgets: the module, compiled once on first use.
	compileOnce sync.Once
	wasm        wazero.CompiledModule
	compileErr  error
}

func loadWidget(spec widget.Spec) (*loadedWidget, error) {
	if !nameRE.MatchString(spec.Name) {
		return nil, fmt.Errorf("widget name %q: want lowercase letters, digits and dashes", spec.Name)
	}
	if (spec.Run == nil) == (spec.Wasm == nil) {
		return nil, fmt.Errorf("widget %s: set exactly one of Run and Wasm", spec.Name)
	}
	if spec.View == nil {
		return nil, fmt.Errorf("widget %s: no View", spec.Name)
	}
	if _, err := fs.Stat(spec.View, "view.js"); err != nil {
		return nil, fmt.Errorf("widget %s: view has no view.js: %w", spec.Name, err)
	}
	sch, err := newSchema(spec.Files)
	if err != nil {
		return nil, fmt.Errorf("widget %s: %w", spec.Name, err)
	}
	if spec.Config != nil {
		if _, err := sch.message(spec.Config.ProtoReflect().Descriptor().FullName()); err != nil {
			return nil, fmt.Errorf("widget %s config: %w", spec.Name, err)
		}
	}
	digest, err := widgetDigest(spec)
	if err != nil {
		return nil, fmt.Errorf("widget %s: %w", spec.Name, err)
	}
	return &loadedWidget{spec: spec, schema: sch, digest: digest}, nil
}

// widgetDigest hashes everything a client caches against: the version, the
// schema, and every file of the view. Any change to any of them is a new
// digest, which both busts the view's cache and discards stored state.
func widgetDigest(spec widget.Spec) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", spec.Name, spec.Version)
	h.Write(spec.Wasm)
	for _, fd := range spec.Files {
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(protodesc.ToFileDescriptorProto(fd))
		if err != nil {
			return "", err
		}
		h.Write(b)
	}
	var names []string
	err := fs.WalkDir(spec.View, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	for _, n := range names {
		b, err := fs.ReadFile(spec.View, n)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

var errStopped = errors.New("instance is not running")

// compiled returns the widget's compiled module, compiling it the first time.
func (w *loadedWidget) compiled(wh *wasmHost) (wazero.CompiledModule, error) {
	w.compileOnce.Do(func() {
		w.wasm, w.compileErr = wh.compile(context.Background(), w.spec.Wasm)
	})
	return w.wasm, w.compileErr
}

func (w *loadedWidget) title() string {
	if w.spec.Title != "" {
		return w.spec.Title
	}
	return w.spec.Name
}
