//go:build linux

package restricteddispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestNativeRunnerWithoutStoreFailsClosed(t *testing.T) {
	runner := &NativeRunner{StartSession: func(context.Context, string) (TextSession, error) {
		t.Fatal("started without authority storage")
		return nil, nil
	}}
	if err := runner.ExecuteRun(t.Context(), "user", "workspace", "chat", "input", func(string, string) error { return nil }); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("missing store not denied: %v", err)
	}
}
