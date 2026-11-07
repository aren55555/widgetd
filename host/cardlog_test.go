package host

import (
	"errors"
	"testing"

	widgetdv1 "github.com/aren55555/widgetd/gen/widgetd/v1"
)

func cur(epoch, seq uint64) *widgetdv1.Cursor { return &widgetdv1.Cursor{Epoch: epoch, Seq: seq} }

func seqs(ps []*widgetdv1.Payload) []uint64 {
	var out []uint64
	for _, p := range ps {
		out = append(out, p.GetCursor().GetSeq())
	}
	return out
}

func state(b string) func() ([]byte, error) { return func() ([]byte, error) { return []byte(b), nil } }

// A log with a snapshot at seq 1 and deltas 2..4.
func filledLog(t *testing.T) *cardLog {
	t.Helper()
	l := newCardLog(7, 10)
	l.putSnapshot("S", []byte("s1"))
	for range 3 {
		if _, err := l.putDelta("D", []byte("d"), "S", state("unused")); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func TestResume(t *testing.T) {
	cases := []struct {
		name       string
		have       *widgetdv1.Cursor
		wantSnap   bool
		wantDeltas []uint64
	}{
		{"first visit gets snapshot and every delta", nil, true, []uint64{2, 3, 4}},
		{"current client gets nothing", cur(7, 4), false, nil},
		{"behind client gets only what it missed", cur(7, 2), false, []uint64{3, 4}},
		{"client at the snapshot gets every delta", cur(7, 1), false, []uint64{2, 3, 4}},
		{"other epoch resyncs from snapshot", cur(6, 4), true, []uint64{2, 3, 4}},
		{"cursor from the future resyncs", cur(7, 99), true, []uint64{2, 3, 4}},
		{"cursor older than the snapshot resyncs", cur(7, 0), true, []uint64{2, 3, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, deltas := filledLog(t).resume(tc.have)
			if (snap != nil) != tc.wantSnap {
				t.Fatalf("snapshot = %v, want %v", snap != nil, tc.wantSnap)
			}
			if got := seqs(deltas); !equal(got, tc.wantDeltas) {
				t.Fatalf("deltas = %v, want %v", got, tc.wantDeltas)
			}
		})
	}
}

func TestResumeEmptyLog(t *testing.T) {
	snap, deltas := newCardLog(1, 4).resume(nil)
	if snap != nil || deltas != nil {
		t.Fatal("an empty log should send nothing")
	}
}

func TestDeltaCarriesPrev(t *testing.T) {
	l := filledLog(t)
	p, _ := l.putDelta("D", nil, "S", state("x"))
	if p.GetPrev().GetSeq() != 4 || p.GetCursor().GetSeq() != 5 {
		t.Fatalf("prev/cursor = %d/%d, want 4/5", p.GetPrev().GetSeq(), p.GetCursor().GetSeq())
	}
}

func TestCompaction(t *testing.T) {
	l := newCardLog(1, 3)
	l.putSnapshot("S", []byte("s1"))
	calls := 0
	current := func() ([]byte, error) { calls++; return []byte("compacted"), nil }
	for range 3 {
		if _, err := l.putDelta("D", nil, "S", current); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("current called %d times, want once when the log filled", calls)
	}
	// The snapshot now stands for seq 4, the delta it was taken after.
	if got := l.snapshot.GetCursor().GetSeq(); got != 4 || string(l.snapshot.GetValue()) != "compacted" {
		t.Fatalf("snapshot at %d = %q", got, l.snapshot.GetValue())
	}
	if snap, deltas := l.resume(cur(1, 4)); snap != nil || len(deltas) != 0 {
		t.Fatal("a client that saw the delta should need nothing after compaction")
	}
	if snap, _ := l.resume(cur(1, 2)); snap == nil {
		t.Fatal("a client behind the compacted snapshot should resync from it")
	}
}

func TestFirstDeltaWithoutSnapshotTakesOne(t *testing.T) {
	l := newCardLog(1, 10)
	if _, err := l.putDelta("D", nil, "S", state("s")); err != nil {
		t.Fatal(err)
	}
	if snap, _ := l.resume(nil); snap == nil || string(snap.GetValue()) != "s" {
		t.Fatal("a new client should get the state the first delta produced")
	}
}

func TestFailedCompactionLeavesLogUnchanged(t *testing.T) {
	l := newCardLog(1, 1)
	_, err := l.putDelta("D", nil, "S", func() ([]byte, error) { return nil, errors.New("boom") })
	if err == nil || l.seq != 0 {
		t.Fatalf("err=%v seq=%d, want an error and no seq consumed", err, l.seq)
	}
}

func equal(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
