package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// Managed admission must preserve the existing recovery of a runtime whose
// entrypoint is already crash-looping. Such a container cannot accept work;
// waiting for it to become a healthy idle runtime leaves every request stuck.
func TestRuntimeUsePreservesRestartBackoffRecovery(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, imageDrift := range []bool{false, true} {
			t.Run(map[bool]string{false: "legacy/", true: "managed/"}[managed]+map[bool]string{false: "same_image", true: "new_image"}[imageDrift], func(t *testing.T) {
				cfg := covRTConfig(t)
				covCrewBindDirs(t, cfg)
				fixture := &covRT{listBody: covExistingList(string(container.StateRestarting)), inspectBody: covRestartingInspect(covRuntimeRef)}
				p := fixture.provider(t, cfg)
				ctx := context.WithValue(t.Context(), managedRuntimeUseKey{}, managed)
				crew := covTeam()
				if imageDrift {
					crew.CachedImage = "sha256:" + strings.Repeat("b", 64)
				}
				id, err := p.EnsureCrewRuntime(ctx, crew)
				if err != nil {
					t.Fatalf("managed restart recovery failed: %v", err)
				}
				if id != "cov-cid-0123456789ab" {
					t.Fatalf("returned crash-looping runtime: %q", id)
				}
				fixture.mu.Lock()
				defer fixture.mu.Unlock()
				removed := false
				for _, id := range fixture.deletes {
					if id == "old-cid" {
						removed = true
					}
				}
				runtimeCreates := 0
				for _, req := range fixture.creates {
					if req.Labels[crewKindLabel] == crewRuntimeKind {
						runtimeCreates++
					}
				}
				if !removed || runtimeCreates != 1 {
					t.Fatalf("runtime was not replaced: deletes=%v creates=%d", fixture.deletes, runtimeCreates)
				}
			})
		}
	}
}
