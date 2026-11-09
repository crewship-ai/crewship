package documents

import (
	"errors"
	"strings"
	"testing"
)

// #5165: a restore replaces the head; the replaced human work must stay
// reachable in History as a checkpoint of the head it replaced, written in the
// same transaction, once, and never at the cost of losing it.
func TestRestoreKeepsReplacedHeadAsCheckpoint(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	l := acquire(t, s)
	if _, e := s.Checkpoint(ctx, key, l, "pinned", []byte("pinned full state"), 0, allow); e != nil {
		t.Fatal(e)
	}
	for i := int64(0); i < 3; i++ {
		if _, e := s.Commit(ctx, key, commit(l, "c"+string(rune('a'+i)), i), allow); e != nil {
			t.Fatal(e)
		}
	}
	gen := int64(0)
	r, e := s.RestoreRequestKeeping(ctx, key, l, "pinned", "restore-1", "fp", 3, &gen, []byte("replaced human work"), allow)
	if e != nil || r.Rev != 4 {
		t.Fatal(r, e)
	}
	list, e := s.Checkpoints(ctx, key, allow)
	// The fixture's initial pin, "pinned" and the kept head.
	if e != nil || len(list) != 3 {
		t.Fatalf("checkpoints %v %v", list, e)
	}
	kept := list[0]
	if kept["rev"] != int64(3) {
		t.Fatalf("the newest checkpoint must be the replaced head at rev 3: %v", kept)
	}
	c, e := s.CheckpointRead(ctx, key, kept["id"].(string), allow)
	if e != nil || string(c.Snapshot) != "replaced human work" || c.Rev != 3 {
		t.Fatal(c, e)
	}
	// A replay of the same restore returns the receipt and writes nothing more.
	again, e := s.RestoreRequestKeeping(ctx, key, l, "pinned", "restore-1", "fp", 3, &gen, []byte("replaced human work"), allow)
	if e != nil || again != r {
		t.Fatal(again, e)
	}
	if list, _ = s.Checkpoints(ctx, key, allow); len(list) != 3 {
		t.Fatalf("replay duplicated the kept head: %v", list)
	}
}

func TestRestoreKeepingSkipsAHeadAlreadyInHistory(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	l := acquire(t, s)
	if _, e := s.Checkpoint(ctx, key, l, "pinned", []byte("pinned full state"), 0, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Commit(ctx, key, commit(l, "one", 0), allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Checkpoint(ctx, key, l, "at-head", []byte("head state"), 1, allow); e != nil {
		t.Fatal(e)
	}
	gen := int64(0)
	if _, e := s.RestoreRequestKeeping(ctx, key, l, "pinned", "restore-1", "fp", 1, &gen, []byte("head state"), allow); e != nil {
		t.Fatal(e)
	}
	if list, _ := s.Checkpoints(ctx, key, allow); len(list) != 3 {
		t.Fatalf("a head already pinned is not pinned twice: %v", list)
	}
}

func TestRestoreKeepingRefusesWhenHistoryCannotHoldTheReplacedHead(t *testing.T) {
	s, _, _ := fixture(t, Options{MaxHistoryBytes: 400})
	l := acquire(t, s)
	if _, e := s.Checkpoint(ctx, key, l, "pinned", []byte("pinned"), 0, allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Commit(ctx, key, commit(l, "one", 0), allow); e != nil {
		t.Fatal(e)
	}
	gen := int64(0)
	big := make([]byte, 1000)
	for i := range big {
		big[i] = 'x'
	}
	if _, e := s.RestoreRequestKeeping(ctx, key, l, "pinned", "restore-1", "fp", 1, &gen, big, allow); !errors.Is(e, ErrQuota) {
		t.Fatalf("a restore that would lose the replaced work must be refused: %v", e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 1 {
		t.Fatalf("refused restore changed the head: %#v %v", v, e)
	}
	if list, _ := s.Checkpoints(ctx, key, allow); len(list) != 2 {
		t.Fatalf("refused restore left a checkpoint: %v", list)
	}
}

func TestRestoreKeepingSkipsAHeadIdenticalToACheckpoint(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	l := acquire(t, s)
	for _, id := range []string{"a", "b"} {
		if _, e := s.Checkpoint(ctx, key, l, id, []byte("state "+id), 0, allow); e != nil {
			t.Fatal(e)
		}
	}
	gen := int64(0)
	// Restore a, then b, then a again with no edits in between: each replaced
	// head is byte-identical to a checkpoint, so History does not grow.
	for i, id := range []string{"a", "b", "a"} {
		if i > 0 {
			l = acquire(t, s) // a restore releases the writer lease
		}
		v, e := s.Read(ctx, key, allow)
		if e != nil {
			t.Fatal(e)
		}
		gen = v.Generation
		if _, e := s.RestoreRequestKeeping(ctx, key, l, id, "restore-"+string(rune('0'+i)), "fp", v.HeadRev, &gen, v.Snapshot, allow); e != nil {
			t.Fatal(i, e)
		}
	}
	list, e := s.Checkpoints(ctx, key, allow)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range list {
		if strings.HasPrefix(c["id"].(string), "replaced-") {
			t.Fatalf("restoring between unchanged checkpoints filled History: %v", list)
		}
	}
}
