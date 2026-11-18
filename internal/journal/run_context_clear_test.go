package journal

import (
	"context"
	"testing"
	"time"
)

func TestWithoutRunIDPreservesRequestLifetimeAndOtherContext(t *testing.T) {
	type fixtureKey struct{}
	parent, cancel := context.WithTimeout(context.WithValue(t.Context(), fixtureKey{}, "kept"), time.Minute)
	defer cancel()
	original := WithRunID(parent, "unrelated-run")
	cleared := WithoutRunID(original)
	if RunIDFromContext(cleared) != "" || RunIDFromContext(original) != "unrelated-run" || cleared.Value(fixtureKey{}) != "kept" {
		t.Fatal("run clearing changed unrelated context or retained attribution")
	}
	wantDeadline, _ := parent.Deadline()
	gotDeadline, ok := cleared.Deadline()
	if !ok || !gotDeadline.Equal(wantDeadline) {
		t.Fatal("run clearing lost the request deadline")
	}
	cancel()
	if cleared.Err() != context.Canceled {
		t.Fatal("run clearing detached request cancellation")
	}
	if WithRunID(original, "") != original {
		t.Fatal("existing empty WithRunID contract changed")
	}
}
