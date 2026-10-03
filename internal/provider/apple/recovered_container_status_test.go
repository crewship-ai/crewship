package apple

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestRecoveredContainerStatusAbsenceRequiresInventory(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		missing      bool
	}{
		{"missing", `if [ "$1" = inspect ]; then exit 1; fi; echo '[]'`, true},
		{"present", `if [ "$1" = inspect ]; then exit 1; fi; echo '[{"configuration":{"id":"cid"},"status":{"state":"running"}}]'`, false},
		{"unavailable", `exit 1`, false},
		{"null_inventory", `if [ "$1" = inspect ]; then exit 1; fi; echo null`, false},
		{"nameless_entry", `if [ "$1" = inspect ]; then exit 1; fi; echo '[{}]'`, false},
		{"null_entry", `if [ "$1" = inspect ]; then exit 1; fi; echo '[null]'`, false},
		{"corrupt", `if [ "$1" = inspect ]; then exit 1; fi; echo '{'`, false},
		{"unknown_state", `echo '[{"configuration":{"id":"cid"},"status":{"state":"unrecognized"}}]'`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeContainer(t, tc.script)
			p := newTestProvider(Config{})
			_, err := p.ContainerStatus(context.Background(), "cid")
			if err == nil {
				t.Fatal("uncertain status accepted")
			}
			if errors.Is(err, provider.ErrContainerNotFound) != tc.missing {
				t.Fatalf("absence classification: %v", err)
			}
		})
	}
}
