// Package widget is the server-half SDK for widgetd widgets.
//
// A widget's server half is a single function, Run, that receives a Host and
// keeps running for as long as its instance is configured. Everything it can
// reach (its configuration, the network, storage, the cards it shows) comes
// through the Host. That is what lets the same code run compiled into a
// server or, later, sandboxed in WASM: only the Host implementation changes.
package widget

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Spec describes a widget: what it is called, the schema both of its halves
// are generated from, its server half, and its built view.
type Spec struct {
	// Name identifies the widget, e.g. "bitcoin-spiral". Lowercase letters,
	// digits and dashes.
	Name    string
	Version string
	// Title is what people call it ("Bitcoin price spiral"); Name if empty.
	Title string

	// Files is the widget's schema: the descriptors generated from its
	// widget.proto. The host decodes and validates every payload and config
	// against these and nothing else.
	Files []protoreflect.FileDescriptor

	// Config is the instance configuration message, or nil if the widget
	// takes none. Only its type matters; pass a zero value.
	Config proto.Message

	// Run is the server half. It should return only when ctx is done, or with
	// an error, after which the host restarts it with backoff.
	Run func(ctx context.Context, h Host) error

	// Wasm is the server half as a WASI module instead, run in a sandbox.
	// Exactly one of Run and Wasm is set.
	Wasm []byte

	// Choices, for a native widget, offers pick-lists for settings fields.
	// A sandboxed widget passes WithChoices to Main instead.
	Choices ChoicesFunc

	// View is the built view: view.js (and view.css, if any) at its root.
	View fs.FS

	// Network is what the widget asks to reach. The host grants at most
	// this, and may grant less.
	Network NetworkRequest
}

// NetworkRequest is a widget's declared need for the network.
type NetworkRequest struct {
	// Hosts it fetches from: "api.example.com", "*.example.com" (exactly
	// one extra label), optionally with ":port".
	Hosts []string
	// Methods it uses; empty means GET only.
	Methods []string
	// Why, in a sentence, shown to whoever grants it.
	Why string
}

// Host is everything a widget's server half can reach.
type Host interface {
	// Config decodes this instance's configuration into dst, which must be
	// the Spec's Config type. The host has already validated it.
	Config(dst proto.Message) error

	// Fetch performs an HTTP request on the widget's behalf, subject to the
	// network policy the host applies to this instance.
	Fetch(req *http.Request) (*http.Response, error)

	// KV is storage private to this instance that survives restarts.
	KV() KV

	// Mount shows a card, or updates the options of one already shown. The id
	// is local to the instance. Payloads are sent through the returned card.
	Mount(id string, opts MountOptions) (RawCard, error)

	// Unmount removes a card.
	Unmount(id string) error

	Logger() *slog.Logger
}

// KV is a small key-value store private to one instance.
type KV interface {
	Get(key string) (value []byte, ok bool, err error)
	Put(key string, value []byte) error
	Delete(key string) error
}

// Layout is a size hint, in grid cells.
type Layout struct {
	Cols, Rows uint32
}

// MountOptions describes a card. StateType and DeltaType are filled in by
// Mount; widgets using RawCard directly set them themselves.
type MountOptions struct {
	Title  string
	Layout Layout
	// VisibleUntil, if set, is when the host unmounts the card by itself, so
	// a card can't outstay its welcome even if the widget stops running.
	VisibleUntil time.Time

	StateType protoreflect.FullName
	DeltaType protoreflect.FullName // empty if the card sends only snapshots
	// ActionType is the message viewers may send the card (a tap, a
	// choice); empty if it takes none. See WithAction and Actions.
	ActionType protoreflect.FullName
}

// RawCard sends serialised payloads. Most widgets use the typed Card instead.
type RawCard interface {
	// Snapshot replaces the card's whole state.
	Snapshot(state []byte) error
	// Delta sends a change. current is called, synchronously and only when
	// the host decides to, for the full state after this delta: the host
	// keeps a snapshot plus the deltas since it, so it never has to apply a
	// delta itself.
	Delta(delta []byte, current func() ([]byte, error)) error

	// Actions delivers what viewers send the card, encoded as its
	// ActionType and already checked against it. Nil if it takes none.
	Actions() <-chan []byte
}

// Card is a typed card: S is its State message, D its Delta message.
type Card[S, D proto.Message] struct {
	raw RawCard
}

// Mount shows a card that sends a State snapshot and then Deltas. The types
// are taken from the type parameters, so the host refuses a payload of any
// other type.
func Mount[S, D proto.Message](h Host, id string, opts MountOptions) (*Card[S, D], error) {
	var s S
	var d D
	opts.StateType = s.ProtoReflect().Descriptor().FullName()
	opts.DeltaType = d.ProtoReflect().Descriptor().FullName()
	raw, err := h.Mount(id, opts)
	if err != nil {
		return nil, err
	}
	return &Card[S, D]{raw: raw}, nil
}

// Snapshot replaces the card's state.
func (c *Card[S, D]) Snapshot(state S) error {
	b, err := proto.Marshal(state)
	if err != nil {
		return err
	}
	return c.raw.Snapshot(b)
}

// Delta sends a change. current must return the full state after the delta;
// it is called only when the host wants a fresh snapshot.
func (c *Card[S, D]) Delta(delta D, current func() S) error {
	b, err := proto.Marshal(delta)
	if err != nil {
		return err
	}
	return c.raw.Delta(b, func() ([]byte, error) { return proto.Marshal(current()) })
}

// StateCard is a card that only ever sends whole states.
type StateCard[S proto.Message] struct {
	raw RawCard
}

// MountState shows a card with no Delta type: every update is a snapshot.
func MountState[S proto.Message](h Host, id string, opts MountOptions) (*StateCard[S], error) {
	var s S
	opts.StateType = s.ProtoReflect().Descriptor().FullName()
	opts.DeltaType = ""
	raw, err := h.Mount(id, opts)
	if err != nil {
		return nil, err
	}
	return &StateCard[S]{raw: raw}, nil
}

// Snapshot replaces the card's state.
func (c *StateCard[S]) Snapshot(state S) error {
	b, err := proto.Marshal(state)
	if err != nil {
		return err
	}
	return c.raw.Snapshot(b)
}

// Raw is the card's untyped side, for Actions.
func (c *Card[S, D]) Raw() RawCard { return c.raw }

// Raw is the card's untyped side, for Actions.
func (c *StateCard[S]) Raw() RawCard { return c.raw }

// WithAction declares that the card accepts actions of type A from viewers.
//
//	card, err := widget.Mount[*pb.State, *pb.Delta](h, "main", widget.WithAction[*pb.Action](opts))
func WithAction[A proto.Message](opts MountOptions) MountOptions {
	var a A
	opts.ActionType = a.ProtoReflect().Descriptor().FullName()
	return opts
}

// Actions decodes a card's actions as A. The channel closes when the card's
// run ends.
func Actions[A proto.Message](card interface{ Raw() RawCard }) <-chan A {
	in := card.Raw().Actions()
	out := make(chan A, cap(in))
	go func() {
		defer close(out)
		for b := range in {
			var a A
			a = a.ProtoReflect().New().Interface().(A)
			if proto.Unmarshal(b, a) == nil {
				out <- a
			}
		}
	}()
	return out
}

// Choice is one option for a settings field.
type Choice struct {
	Value string // what the field is set to
	Label string // what the list shows; Value if empty
}

// ChoicesFunc offers pick-lists for settings fields, given the settings as
// edited so far: read them with h.Config (they are unsaved, and may not yet
// pass their rules), fetch what you need with h.Fetch, and return choices
// keyed by field name ("team"; dotted for nested fields). A field you don't
// return stays free text. It runs briefly, while someone edits settings,
// and must not mount cards.
type ChoicesFunc func(ctx context.Context, h Host) (map[string][]Choice, error)

// MainOption configures Main.
type MainOption func(*mainOptions)

type mainOptions struct {
	choices ChoicesFunc
}

// WithChoices gives a sandboxed widget pick-lists for its settings form.
//
//	func main() { widget.Main(server.Run, widget.WithChoices(server.Choices)) }
func WithChoices(f ChoicesFunc) MainOption {
	return func(o *mainOptions) { o.choices = f }
}
