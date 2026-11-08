package host

import (
	"errors"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

// cardLog is what the host remembers of one card's data: the latest snapshot
// and every delta since it. That is enough to bring any client up to date
// without the host ever applying a delta itself, which it couldn't do anyway:
// it knows a widget's schema, not its semantics.
//
// Every update takes the next seq. A snapshot taken for compaction carries
// the seq of the delta it was taken after, since it is that same state.
type cardLog struct {
	epoch     uint64
	seq       uint64
	snapshot  *widgetdv1.Payload   // nil until the first one
	deltas    []*widgetdv1.Payload // after snapshot, oldest first
	maxDeltas int
}

func newCardLog(epoch uint64, maxDeltas int) *cardLog {
	if maxDeltas < 1 {
		maxDeltas = 1
	}
	return &cardLog{epoch: epoch, maxDeltas: maxDeltas}
}

func (l *cardLog) cursor() *widgetdv1.Cursor {
	return &widgetdv1.Cursor{Epoch: l.epoch, Seq: l.seq}
}

// putSnapshot records a whole new state and returns the payload to broadcast.
func (l *cardLog) putSnapshot(typeURL string, value []byte) *widgetdv1.Payload {
	l.seq++
	p := &widgetdv1.Payload{TypeUrl: typeURL, Value: value, Cursor: l.cursor()}
	l.snapshot = p
	l.deltas = nil
	return p
}

// putDelta records a change and returns the payload to broadcast. When the
// log is full it asks current for the state after this delta and starts over
// from it; that snapshot is not broadcast, since every listener already has
// the delta it is equivalent to.
func (l *cardLog) putDelta(typeURL string, value []byte, stateTypeURL string, current func() ([]byte, error)) (*widgetdv1.Payload, error) {
	prev := l.cursor()
	l.seq++
	p := &widgetdv1.Payload{TypeUrl: typeURL, Value: value, Cursor: l.cursor(), Prev: prev}
	if l.snapshot == nil || len(l.deltas)+1 >= l.maxDeltas {
		state, err := current()
		if err != nil {
			l.seq--
			return nil, err
		}
		l.snapshot = &widgetdv1.Payload{TypeUrl: stateTypeURL, Value: state, Cursor: l.cursor()}
		l.deltas = nil
		return p, nil
	}
	l.deltas = append(l.deltas, p)
	return p, nil
}

// putDeltaDeferred records a change from a widget the host can't call back
// into (a sandboxed one): instead of asking for the current state inline, it
// reports whether a checkpoint is wanted, and the widget sends one next.
// Past twice the usual length, with no checkpoint, it refuses: a log that
// only grows is a widget ignoring the protocol.
func (l *cardLog) putDeltaDeferred(typeURL string, value []byte) (p *widgetdv1.Payload, wantCheckpoint bool, err error) {
	if len(l.deltas) >= 2*l.maxDeltas {
		return nil, true, errCheckpointOverdue
	}
	prev := l.cursor()
	l.seq++
	p = &widgetdv1.Payload{TypeUrl: typeURL, Value: value, Cursor: l.cursor(), Prev: prev}
	l.deltas = append(l.deltas, p)
	return p, l.snapshot == nil || len(l.deltas) >= l.maxDeltas, nil
}

// checkpoint records the state as of the latest update, without a new seq:
// it is that same state, which every listener already has.
func (l *cardLog) checkpoint(typeURL string, value []byte) {
	l.snapshot = &widgetdv1.Payload{TypeUrl: typeURL, Value: value, Cursor: l.cursor()}
	l.deltas = nil
}

var errCheckpointOverdue = errors.New("too many deltas without a checkpoint")

// resume returns what a client holding cursor `have` (nil: nothing) needs to
// be current: nothing, the deltas after its cursor, or the snapshot and every
// delta after that.
func (l *cardLog) resume(have *widgetdv1.Cursor) (snapshot *widgetdv1.Payload, deltas []*widgetdv1.Payload) {
	if l.snapshot == nil {
		return nil, nil
	}
	if have != nil && have.GetEpoch() == l.epoch && have.GetSeq() >= l.snapshot.GetCursor().GetSeq() && have.GetSeq() <= l.seq {
		for i, d := range l.deltas {
			if d.GetCursor().GetSeq() > have.GetSeq() {
				return nil, l.deltas[i:]
			}
		}
		return nil, nil
	}
	return l.snapshot, l.deltas
}
