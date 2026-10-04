package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestImageReferenceTagToSameDigestReusesRunningRuntime(t *testing.T) {
	p, calls := newDriftFixture(t, "crewship-team-safe-safe", "crewship-cache:old-tag")
	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "safe", Slug: "safe", CachedImage: "sha256:" + strings.Repeat("a", 64)})
	if err != nil || id == "" {
		t.Fatalf("same artifact blocked: id=%q err=%v", id, err)
	}
	if len(calls.snapshot()) != 0 {
		t.Fatalf("same artifact changed runtime: %v", calls.snapshot())
	}
}
