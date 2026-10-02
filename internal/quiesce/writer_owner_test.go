package quiesce

import (
	"errors"
	"testing"
)

func TestBeginRejectsLostWriterOwner(t *testing.T) {
	c := New()
	if err := c.SetWriterOwner(func() error { return errors.New("lost") }); err != nil {
		t.Fatal(err)
	}
	w, err := c.Begin(t.Context(), Options{})
	if w != nil {
		w.Release()
	}
	if !errors.Is(err, ErrWriterOwnerLost) {
		t.Fatalf("Begin = %v, want ownership error", err)
	}
	if c.Holding() {
		t.Fatal("invalid owner held a window")
	}
}

func TestBeginRechecksOwnerAfterAcquiringGuards(t *testing.T) {
	c := New()
	lost := false
	released := false
	if err := c.SetWriterOwner(func() error {
		if lost {
			return errors.New("lost after drain")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w, err := c.Begin(t.Context(), Options{Acquire: func() (func(), error) {
		lost = true
		return func() { released = true }, nil
	}})
	if w != nil {
		w.Release()
	}
	if !errors.Is(err, ErrWriterOwnerLost) || !released || c.Holding() {
		t.Fatalf("invalid owner: %v released=%v holding=%v", err, released, c.Holding())
	}
}
