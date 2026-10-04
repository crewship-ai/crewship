package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

type backupPreparationProvider struct {
	provider.ContainerProvider
	calls int
}

func (p *backupPreparationProvider) PrepareExternalExec(_ context.Context, id string, cmd, env []string) ([]string, []string, func(context.Context) error, func(context.Context) error, error) {
	p.calls++
	return append([]string{"gate", id}, cmd...), []string{"SYNTHETIC=env"}, nil, nil, nil
}
func (p *backupPreparationProvider) GuardExternalExec(context.Context, string) (func(context.Context) error, func(context.Context) error, error) {
	panic("duplicate legacy guard")
}

type backupLegacyGuardProvider struct {
	provider.ContainerProvider
	calls int
}

func (p *backupLegacyGuardProvider) GuardExternalExec(context.Context, string) (func(context.Context) error, func(context.Context) error, error) {
	p.calls++
	return nil, nil, nil
}

func TestBackupRouterResolvesPreparationPerExec(t *testing.T) {
	r := &Router{}
	ops := r.backupDockerOps(nil)
	cmd := []string{"sync"}
	got, env, _, _, err := ops.Prepare(t.Context(), "owned", cmd, nil)
	if err != nil || !reflect.DeepEqual(got, cmd) || env != nil {
		t.Fatal("no-provider behavior changed")
	}
	prepared := &backupPreparationProvider{}
	r.keeperContainer = prepared
	got, env, _, _, err = ops.Prepare(t.Context(), "owned", cmd, nil)
	if err != nil || prepared.calls != 1 || !reflect.DeepEqual(got, []string{"gate", "owned", "sync"}) || !reflect.DeepEqual(env, []string{"SYNTHETIC=env"}) {
		t.Fatalf("active provider not resolved: %v %v %v", got, env, err)
	}
	legacy := &backupLegacyGuardProvider{}
	r.keeperContainer = legacy
	got, env, _, _, err = ops.Prepare(t.Context(), "owned", cmd, nil)
	if err != nil || legacy.calls != 1 || !reflect.DeepEqual(got, cmd) || env != nil {
		t.Fatal("legacy guard fallback changed")
	}
}
