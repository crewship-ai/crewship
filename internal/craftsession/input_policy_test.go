package craftsession

import (
	"context"
	"errors"
	"testing"
)

// A person's undo writes with its own input policy: the session's default policy, which refuses
// every formula, would turn the undo of a cleared formula column into an engine failure.
func TestAWriteFollowsThePolicyOfItsContext(t *testing.T) {
	ctx := context.Background()
	m := newMgr(t, nil)
	s, release, err := m.Acquire(ctx, "doc1", doc(""))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	block := [][]any{{"=A1+1"}, {"text"}}
	if _, err := s.WriteRange(ctx, "B1:B2", block); !errors.Is(err, ErrPolicy) {
		t.Fatalf("the default policy let a formula through: %v", err)
	}
	allowAll := WithInputPolicy(ctx, func(string) error { return nil })
	if _, err := s.WriteRange(allowAll, "B1:B2", block); err != nil {
		t.Fatalf("a block under the context's policy: %v", err)
	}
	if _, err := s.Apply(allowAll, []WriteOp{{Range: "C1:C2", Values: block}, {Cell: "D1", Input: "=B1*2"}}); err != nil {
		t.Fatalf("a batch under the context's policy: %v", err)
	}
	if err := s.SetCell(allowAll, "E1", "=B1"); err != nil {
		t.Fatalf("a cell under the context's policy: %v", err)
	}
	if err := s.SetCell(ctx, "E2", "=B1"); !errors.Is(err, ErrPolicy) {
		t.Fatalf("the context's policy outlived its context: %v", err)
	}
}
