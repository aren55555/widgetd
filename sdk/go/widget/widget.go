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
	"strings"
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
	// Icon is drawn beside Title on widgetd's own pages: the board's card
	// headers and the admin pages. Optional.
	Icon *Icon

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
	// Secrets are Config fields (top-level strings) that are write-only:
	// stored sealed, never shown again, and given only to the widget.
	Secrets []string

	// Modes are the helper modes a Wasm module implements: "choices",
	// "verify". The host only runs a module in a mode it declares, since a
	// module that doesn't know one would just start its main loop.
	Modes []string

	// Choices, for a native widget, offers pick-lists for settings fields.
	// A sandboxed widget passes WithChoices to Main instead.
	Choices ChoicesFunc

	// Verify, for a native widget, checks settings before they're saved.
	// A sandboxed widget passes WithVerify to Main instead.
	Verify VerifyFunc

	// View is the built view: view.js (and view.css, if any) at its root.
	View fs.FS

	// Uses are the accounts the widget's requests may be signed with, and
	// with what scopes. The display has one account per provider; its
	// sign-in covers exactly the union of the scopes its widgets use.
	Uses []Use

	// Network is what the widget asks to reach. The host grants at most
	// this, and may grant less.
	Network NetworkRequest
}

// Icon is a Lucide icon as drawing data: shapes on a 24×24 viewBox, stroked
// in currentColor. widgetc fills it from the `icon` named in widget.toml.
type Icon struct {
	// Name is Lucide's name for it, e.g. "cloud-sun".
	Name   string      `json:"name"`
	Shapes []IconShape `json:"shapes"`
}

// IconShape is one element of an Icon: a tag such as "path" and its
// geometry attributes, e.g. {"d": "M12 2v2"}.
type IconShape struct {
	Tag   string            `json:"tag"`
	Attrs map[string]string `json:"attrs"`
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
	verify  VerifyFunc
}

// WithChoices gives a sandboxed widget pick-lists for its settings form.
//
//	func main() { widget.Main(server.Run, widget.WithChoices(server.Choices)) }
func WithChoices(f ChoicesFunc) MainOption {
	return func(o *mainOptions) { o.choices = f }
}

// Violation is a problem with one settings field ("" for the whole thing).
type Violation struct {
	Field   string
	Message string
}

// Verdict is what Verify decides.
type Verdict struct {
	// Violations, if any, stop the save and show beside their fields.
	Violations []Violation
	// Config, if set, is saved instead: the same settings tidied up. It must
	// still pass the schema's rules.
	Config proto.Message
}

// VerifyFunc checks settings someone is about to save, beyond what the
// schema's rules can say: that a team exists, that an account answers. Read
// them with h.Config (they already pass the rules); h.Fetch works as ever.
// Return an error only if the check itself couldn't be made (the save is
// then refused with that reason, so it can be retried); a problem with the
// settings is a Violation. It runs briefly and must not mount cards.
type VerifyFunc func(ctx context.Context, h Host) (Verdict, error)

// WithVerify gives a sandboxed widget a settings check.
//
//	func main() { widget.Main(server.Run, widget.WithVerify(server.Verify)) }
func WithVerify(f VerifyFunc) MainOption {
	return func(o *mainOptions) { o.verify = f }
}

// Use is an account a widget uses: a provider the host knows ("google",
// "github") and the scopes it needs there. The host runs the sign-in and
// keeps the token; the widget marks requests with Authorize and the host
// signs them, only for that provider's API hosts.
type Use struct {
	Provider string   `json:"provider" toml:"provider"`
	Scopes   []string `json:"scopes" toml:"scopes"`
}

// authorizeHeader marks a request for the host to sign in. The host strips
// it, so it never leaves the machine.
const authorizeHeader = "Widgetd-Authorize"

// Authorize marks req to carry the signed-in account's token. The host adds
// it, only if req goes to an API host of a provider the widget uses, and the widget
// never sees the token itself. A 401 back means the sign-in has lapsed:
// the host stops the instance and asks for it again.
func Authorize(req *http.Request) *http.Request {
	req.Header.Set(authorizeHeader, "1")
	return req
}

// IsAuthorized reports whether req was marked by Authorize, and unmarks it.
// For hosts implementing Fetch.
func IsAuthorized(req *http.Request) bool {
	ok := req.Header.Get(authorizeHeader) != ""
	req.Header.Del(authorizeHeader)
	return ok
}

// ScopeError is a request that needed a scope the widget didn't declare:
// either the host wouldn't sign it (no declared scope covers it), or the
// API refused it for a missing scope. Declare the scope in widget.toml's
// [[uses]]; the host never widens a grant on its own.
type ScopeError struct {
	Provider string   // "Google"
	Missing  []string // scopes that would have covered it, if known
	// Refused is true when the API refused the request, false when the
	// host didn't sign it.
	Refused bool
}

func (e *ScopeError) Error() string {
	what := "the host didn't sign this request"
	if e.Refused {
		what = e.Provider + " refused this request"
	}
	if len(e.Missing) == 0 {
		return what + ": it needs a scope the widget didn't declare"
	}
	return what + ": it needs " + strings.Join(e.Missing, " or ") + ", which the widget didn't declare"
}
