//go:build livedocker

package backup

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestLive_RestoreOrdinaryFilesAndMissingMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	lc := startLiveCrew(ctx, t, cli, fmt.Sprintf("acceptance-2777-%d", time.Now().UnixNano()), true)
	ops := &MobyDockerOps{Client: cli}
	crew := CrewTarget{ID: "acceptance", Slug: "acceptance", ContainerID: lc.id}
	lc.mustSh(ctx, t, "1001:1001", `printf ordinary > /crew/shared/ordinary.txt; printf workspace > /workspace/probe.txt`)
	plain, _ := collectToPayload(ctx, t, ops, crew, ScopeLevelStandard, true)
	lc.mustSh(ctx, t, "1001:1001", `rm /crew/shared/ordinary.txt /workspace/probe.txt`)
	if err := RestoreCrew(ctx, ops, lc.id, crew.Slug, plain); err != nil {
		t.Fatalf("ordinary files without memory: %v", err)
	}
	if got := lc.stat(ctx, t, "/crew/shared/ordinary.txt"); got.content != "ordinary" {
		t.Fatalf("ordinary file: %+v", got)
	}
	lc.mustSh(ctx, t, "1001:1001", `mkdir -p /crew/shared/.memory/nested; printf memory > /crew/shared/.memory/nested/fact.txt`)
	withMemory, _ := collectToPayload(ctx, t, ops, crew, ScopeLevelStandard)
	lc.mustSh(ctx, t, "1001:1001", `rm -rf /crew/shared/.memory`)
	if err := RestoreCrew(ctx, ops, lc.id, crew.Slug, withMemory); err != nil {
		t.Fatalf("missing memory directories: %v", err)
	}
	if got := lc.stat(ctx, t, "/crew/shared/.memory/nested/fact.txt"); got.content != "memory" {
		t.Fatalf("memory file: %+v", got)
	}
	// A second restore must also succeed into the now sidecar-owned files.
	if err := RestoreCrew(ctx, ops, lc.id, crew.Slug, withMemory); err != nil {
		t.Fatalf("repeat restore: %v", err)
	}
}
