package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The guard runs before anything reaches the daemon: a refusal returns
// before ExecCreate (the nil Client would panic if it were reached).
func TestMobyDockerOpsGuardRefusesBeforeCreate(t *testing.T) {
	refused := errors.New("egress fence not in place")
	var seen string
	ops := &MobyDockerOps{Guard: func(_ context.Context, id string) (func(context.Context) error, func(context.Context) error, error) {
		seen = id
		return nil, nil, refused
	}}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"ExecAs", func() error {
			_, _, err := ops.ExecAs(context.Background(), "crew-c1", "1001:1001", []string{"true"})
			return err
		}},
		{"Exec", func() error { _, _, err := ops.Exec(context.Background(), "crew-c1", []string{"sync"}); return err }},
		{"CopyToPath", func() error {
			return ops.CopyToPath(context.Background(), "crew-c1", ExtractSpec{Dest: "/crew", User: "1002:1002"}, strings.NewReader(""))
		}},
	} {
		seen = ""
		err := tc.run()
		if !errors.Is(err, refused) {
			t.Fatalf("%s: got %v, want the guard's refusal", tc.name, err)
		}
		if seen != "crew-c1" {
			t.Fatalf("%s: guard saw %q", tc.name, seen)
		}
	}
}

func TestMobyDockerOpsNilGuardFuncsAreNoops(t *testing.T) {
	ops := &MobyDockerOps{Guard: func(context.Context, string) (func(context.Context) error, func(context.Context) error, error) {
		return nil, nil, nil
	}}
	before, after, err := ops.guard(context.Background(), "c")
	if err != nil || before(context.Background()) != nil || after(context.Background()) != nil {
		t.Fatalf("nil checks must be no-ops: %v", err)
	}
}
