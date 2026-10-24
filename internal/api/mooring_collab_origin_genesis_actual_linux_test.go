//go:build linux && mooring_rust_integration

package api

import "testing"

// Actual pinned private runtime checkpoint description + FULL SQL + canonical
// original human source. This creates correlation only, not an issued origin.
func TestCollaborationOriginNodeActualOriginalInitialization(t *testing.T) {
	t.Run("fresh", func(t *testing.T) { runCollaborationSessionActualStartup(t, false, true) })
	t.Run("source-ABA", func(t *testing.T) { runCollaborationSessionActualStartup(t, true, true) })
}
