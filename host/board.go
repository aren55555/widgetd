package host

import (
	"sort"
	"sync"
	"time"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

// subscriberBuffer is how many frames a listener may fall behind by. Past
// that it is disconnected rather than allowed to hold anything up; it
// reconnects with its cursors and is sent only what it missed.
const subscriberBuffer = 1024

// card is one card on the board.
type card struct {
	instanceID string
	id         string // full id: instanceID + "/" + local id
	order      uint64 // mount order, for a stable layout
	epoch      uint64
	digest     string
	mounted    *widgetdv1.Mounted
	stateURL   string
	deltaURL   string
	log        *cardLog
	expiry     *time.Timer
}

// subscriber is one open stream.
type subscriber struct {
	ch   chan *widgetdv1.WidgetFrame
	gone chan struct{} // closed when the board drops it for falling behind
	once sync.Once
}

func (s *subscriber) drop() { s.once.Do(func() { close(s.gone) }) }

// board is every mounted card and every open stream. All changes to a card
// and their broadcast happen under one lock, so a new subscriber's initial
// frames and its live frames never overlap or leave a gap.
type board struct {
	mu    sync.Mutex
	order uint64
	cards map[string]*card
	subs  map[*subscriber]struct{}
}

func newBoard() *board {
	return &board{cards: map[string]*card{}, subs: map[*subscriber]struct{}{}}
}

func (b *board) broadcastLocked(f *widgetdv1.WidgetFrame) {
	for s := range b.subs {
		select {
		case s.ch <- f:
		default:
			delete(b.subs, s)
			s.drop()
		}
	}
}

func mountedFrame(c *card) *widgetdv1.WidgetFrame {
	return &widgetdv1.WidgetFrame{
		InstanceId: c.instanceID,
		CardId:     c.id,
		Body:       &widgetdv1.WidgetFrame_Mounted{Mounted: c.mounted},
	}
}

// mount adds a card, or replaces the one with the same id.
func (b *board) mount(c *card) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if old, ok := b.cards[c.id]; ok {
		if old.expiry != nil {
			old.expiry.Stop()
		}
		c.order = old.order
	} else {
		b.order++
		c.order = b.order
	}
	b.cards[c.id] = c
	b.broadcastLocked(mountedFrame(c))
}

// unmount removes a card if it is still the one given (nil: whichever).
func (b *board) unmount(id string, only *card, purge bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.cards[id]
	if !ok || (only != nil && c != only) {
		return false
	}
	if c.expiry != nil {
		c.expiry.Stop()
	}
	delete(b.cards, id)
	b.broadcastLocked(&widgetdv1.WidgetFrame{
		InstanceId: c.instanceID,
		CardId:     id,
		Body:       &widgetdv1.WidgetFrame_Unmounted{Unmounted: &widgetdv1.Unmounted{Purge: purge}},
	})
	return true
}

// unmountInstance removes every card of an instance.
func (b *board) unmountInstance(instanceID string, purge bool) {
	b.mu.Lock()
	var ids []string
	for id, c := range b.cards {
		if c.instanceID == instanceID {
			ids = append(ids, id)
		}
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.unmount(id, nil, purge)
	}
}

// snapshot records and broadcasts a new state for card c, if c is still
// mounted.
func (b *board) snapshot(c *card, value []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cards[c.id] != c {
		return false
	}
	p := c.log.putSnapshot(c.stateURL, value)
	b.broadcastLocked(&widgetdv1.WidgetFrame{
		InstanceId: c.instanceID, CardId: c.id,
		Body: &widgetdv1.WidgetFrame_Snapshot{Snapshot: p},
	})
	return true
}

// delta records and broadcasts a change for card c, if c is still mounted.
func (b *board) delta(c *card, value []byte, current func() ([]byte, error)) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cards[c.id] != c {
		return false, nil
	}
	p, err := c.log.putDelta(c.deltaURL, value, c.stateURL, current)
	if err != nil {
		return true, err
	}
	b.broadcastLocked(&widgetdv1.WidgetFrame{
		InstanceId: c.instanceID, CardId: c.id,
		Body: &widgetdv1.WidgetFrame_Delta{Delta: p},
	})
	return true, nil
}

// fault tells every screen that card c has a problem, without unmounting it.
func (b *board) fault(c *card, msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cards[c.id] != c {
		return
	}
	b.broadcastLocked(&widgetdv1.WidgetFrame{
		InstanceId: c.instanceID, CardId: c.id,
		Body: &widgetdv1.WidgetFrame_Fault{Fault: &widgetdv1.Fault{Message: msg}},
	})
}

// subscribe registers a listener and returns, atomically with that, the
// frames that bring a client holding `resume` up to date: every card's
// Mounted, then whatever data it is missing.
func (b *board) subscribe(resume map[string]*widgetdv1.Resume) ([]*widgetdv1.WidgetFrame, *subscriber, func()) {
	s := &subscriber{ch: make(chan *widgetdv1.WidgetFrame, subscriberBuffer), gone: make(chan struct{})}
	b.mu.Lock()
	defer b.mu.Unlock()

	cards := make([]*card, 0, len(b.cards))
	for _, c := range b.cards {
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].order < cards[j].order })

	var initial []*widgetdv1.WidgetFrame
	for _, c := range cards {
		initial = append(initial, mountedFrame(c))
		var have *widgetdv1.Cursor
		if r, ok := resume[c.id]; ok && r.GetDigest() == c.digest {
			have = r.GetCursor()
		}
		snap, deltas := c.log.resume(have)
		if snap != nil {
			initial = append(initial, &widgetdv1.WidgetFrame{
				InstanceId: c.instanceID, CardId: c.id,
				Body: &widgetdv1.WidgetFrame_Snapshot{Snapshot: snap},
			})
		}
		for _, d := range deltas {
			initial = append(initial, &widgetdv1.WidgetFrame{
				InstanceId: c.instanceID, CardId: c.id,
				Body: &widgetdv1.WidgetFrame_Delta{Delta: d},
			})
		}
	}
	b.subs[s] = struct{}{}
	return initial, s, func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}
}

// deltaDeferred is delta for a widget that checkpoints separately.
func (b *board) deltaDeferred(c *card, value []byte) (wantCheckpoint, ok bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cards[c.id] != c {
		return false, false, nil
	}
	p, want, err := c.log.putDeltaDeferred(c.deltaURL, value)
	if err != nil {
		return true, true, err
	}
	b.broadcastLocked(&widgetdv1.WidgetFrame{
		InstanceId: c.instanceID, CardId: c.id,
		Body: &widgetdv1.WidgetFrame_Delta{Delta: p},
	})
	return want, true, nil
}

// checkpoint stores the card's current state as its new starting point. It
// is not broadcast: every listener already has the updates it sums up.
func (b *board) checkpoint(c *card, value []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cards[c.id] != c {
		return false
	}
	c.log.checkpoint(c.stateURL, value)
	return true
}
