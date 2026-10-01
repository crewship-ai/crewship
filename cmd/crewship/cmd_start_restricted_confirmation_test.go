//go:build linux && !clionly

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
)

type confirmationAuthority struct{}

func (confirmationAuthority) Resolve(context.Context, string) (restrictedruntime.Plan, error) {
	return restrictedruntime.Plan{}, restrictedruntime.ErrDenied
}
func (confirmationAuthority) Secrets(context.Context, string) (map[string]string, error) {
	return nil, restrictedruntime.ErrDenied
}
func (confirmationAuthority) Volume(context.Context, restrictedruntime.Plan, restrictedruntime.Mount) (string, error) {
	return "", restrictedruntime.ErrDenied
}

func TestRestrictedRegistryConfirmsEveryDurableStopIdentity(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	store := work.NewStore(db)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.AcceptTx(t.Context(), tx, work.AcceptRequest{WorkspaceID: "confirmation-w", Source: work.SourceManual, DomainKind: "restricted_workflow", DomainID: "owned-workflow", Class: work.ClassBackground})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "test", Limits: work.SerialAgentLimits(), WorkID: receipt.WorkID})
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []string{"owned-root", "owned-child"} {
		if _, err = db.Exec(`INSERT INTO restricted_workflow_attempt_roots(access_attempt_id,workflow_id,run_id,generation) VALUES(?,'owned-workflow',?,?)`, attempt, claimed.RunID, claimed.Generation); err != nil {
			t.Fatal(err)
		}
	}
	// No live access/domain rows are needed to retain stop evidence after removal.
	dir := t.TempDir()
	binary := filepath.Join(dir, "docker")
	if err = os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$*\" in *owned-child*) echo synthetic-container;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	manager, err := restrictedruntime.New(filepath.Join(dir, "state"), restrictedruntime.Docker{Binary: binary}, confirmationAuthority{}, confirmationAuthority{}, restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	registry := &restrictedExecutor{db: db, managers: []*restrictedruntime.Manager{manager}}
	if err = registry.ConfirmWorkflowStopped(t.Context(), "owned-workflow"); err == nil {
		t.Fatal("a surviving child was reported stopped")
	}
	if err = os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = registry.ConfirmWorkflowStopped(t.Context(), "owned-workflow"); err != nil {
		t.Fatalf("owned absence not confirmed: %v", err)
	}
	if err = os.WriteFile(binary, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = registry.ConfirmWorkflowStopped(t.Context(), "owned-workflow"); err == nil {
		t.Fatal("transport failure was reported as stopped")
	}
}
