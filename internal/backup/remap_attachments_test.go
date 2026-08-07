package backup

// What a --as-workspace restore does to an attachment row (#1791 review).
//
// This is a REACHABILITY pin, not a bug report against this package. RemapIDs
// rewrites primary keys and every FK column, which is exactly its contract, so
// a restored attachment row lands with a NEW workspace_id and its original
// storage_key — a plain TEXT column nothing remaps — still spelling the workspace
// the bundle came from.
//
// internal/api relies on that: the attachment refcount cannot require
// storage_key to equal the key reconstructed from (workspace_id, sha256),
// because these rows exist and their bytes are still resolved from
// (workspace_id, sha256) on every download. See
// attachmentContentAddressedPredicate.
//
// If this test ever fails because storage_key IS remapped, that is good news and
// the second branch of that predicate can be revisited — deliberately, with this
// comment in front of whoever revisits it.

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestRemapIDs_LeavesAttachmentStorageKeySpellingTheOldWorkspace(t *testing.T) {
	db := testutil.MigratedSQLDB(t)

	const oldWS = "ws-bundle-origin"
	const sha = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	storageKey := "attachments/" + oldWS + "/" + sha[:2] + "/" + sha

	dump := &DBDump{
		WorkspaceID: oldWS,
		Tables: map[string][]map[string]any{
			"workspaces": {
				{"id": oldWS, "name": "Origin", "slug": "origin"},
			},
			"attachments": {
				{
					"id":           "att-restored",
					"workspace_id": oldWS,
					"owner_type":   "issue",
					"sha256":       sha,
					"storage_key":  storageKey,
					"filename":     "restored.log",
				},
			},
		},
	}

	if err := RemapIDs(context.Background(), db, dump); err != nil {
		t.Fatalf("RemapIDs: %v", err)
	}

	row := dump.Tables["attachments"][0]
	newWS, _ := row["workspace_id"].(string)
	if newWS == "" || newWS == oldWS {
		t.Fatalf("workspace_id = %q — the FK was not remapped, so this test is not "+
			"describing a --as-workspace restore", newWS)
	}
	gotKey, _ := row["storage_key"].(string)
	if gotKey != storageKey {
		t.Fatalf("storage_key = %q, want it left at %q", gotKey, storageKey)
	}
	// The consequence, spelled out: the key no longer matches what the row's own
	// (workspace_id, sha256) reconstructs — which is the shape the attachment
	// refcount has to keep counting.
	reconstructed := "attachments/" + newWS + "/" + sha[:2] + "/" + sha
	if strings.EqualFold(gotKey, reconstructed) {
		t.Fatalf("storage_key and the reconstruction agree (%q) — the drifted-key case is "+
			"unreachable through this path", gotKey)
	}
}
