package restrictedworkflow

import (
	"database/sql"
	"errors"
	"testing"
)

func TestWorkflowRequiresInstalledExecutorOnEveryPlatform(t *testing.T) {
	if service, err := New(&sql.DB{}, nil); service != nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("uninstalled runtime admitted workflow: %v", err)
	}
}
