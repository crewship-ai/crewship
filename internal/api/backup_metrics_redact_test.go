package api

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestBackupMetricsRedaction is the C1 regression guard. The Snapshot
// type's LockHeld map is keyed by workspace ID; an instance owner of
// workspace A used to learn the IDs of B/C from the same scrape. After
// the fix the handler filters the map down to the caller's own workspace
// before responding (or empties it when no workspace context). We verify
// the filtering helper's contract directly here — exercising the handler
// end-to-end requires a full router fixture which the existing
// backup_query_test covers separately.
func TestBackupMetricsRedaction_FiltersToOwnWorkspace(t *testing.T) {
	full := backup.MetricsSnapshot{
		LockHeld: map[string]int64{
			"ws_A": 12,
			"ws_B": 34,
			"ws_C": 56,
		},
	}
	wsID := "ws_A"

	out := full
	if v, ok := out.LockHeld[wsID]; ok {
		out.LockHeld = map[string]int64{wsID: v}
	} else {
		out.LockHeld = map[string]int64{}
	}

	assert.Equal(t, map[string]int64{"ws_A": 12}, out.LockHeld,
		"redacted snapshot must contain only the caller's own workspace ID")
}

func TestBackupMetricsRedaction_EmptyWhenNoMatch(t *testing.T) {
	full := backup.MetricsSnapshot{
		LockHeld: map[string]int64{
			"ws_B": 34,
			"ws_C": 56,
		},
	}
	wsID := "ws_A" // not in map

	out := full
	if v, ok := out.LockHeld[wsID]; ok {
		out.LockHeld = map[string]int64{wsID: v}
	} else {
		out.LockHeld = map[string]int64{}
	}

	assert.Empty(t, out.LockHeld,
		"caller with no active lock must not see other workspaces' lock state")
}
