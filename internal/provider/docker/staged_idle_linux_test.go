//go:build linux

package docker

import (
	"context"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/provider"
	"os"
	"testing"
)

func TestStagedLifecycleRevisionActivatesOnlyAtVerifiedIdle(t *testing.T) {
	if !lifecycleHostSupported(t) {
		return
	}
	for _, idle := range []bool{false, true} {
		t.Run(fmt.Sprint(idle), func(t *testing.T) {
			f := newQualifiedFixture(t, true)
			if e := os.Remove(f.p.cfg.EntrypointPath); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(f.p.cfg.EntrypointPath, []byte("#!/bin/sh\n#new revision\nexit 0\n"), 0555); e != nil {
				t.Fatal(e)
			}
			verified := 0
			f.p.SetRuntimeIdleVerifier(func(context.Context, string, string) error {
				verified++
				if !idle {
					return errors.New("active recovered run")
				}
				return nil
			})
			use, e := f.p.AcquireCrewRuntimeUse(t.Context(), f.crew)
			if use != nil {
				use.Release()
			}
			if idle {
				if e != nil || use == nil || verified != 1 || f.stops != 1 || f.deletes != 1 || f.removeForce || f.removeVolumes {
					t.Fatalf("verified idle did not activate safely: err=%v verified=%d stops=%d removes=%d", e, verified, f.stops, f.deletes)
				}
			} else if !errors.Is(e, provider.ErrRuntimeImageUpdatePending) || f.stops != 0 || f.deletes != 0 {
				t.Fatalf("active work interrupted: err=%v stops=%d removes=%d", e, f.stops, f.deletes)
			}
		})
	}
}
