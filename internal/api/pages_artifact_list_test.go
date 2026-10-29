package api

import (
	"strings"
	"testing"
)

// The Artifacts library lists versioned artifacts from the page index: which
// kind of version head a Page carries and the version it shares. The latest
// version is a writer's fact (a reader sees only the shared pin), so the list
// never carries it.
func TestPagesListArtifactKindAndSharedVersion(t *testing.T) {
	rig := pagesTransferSetup(t)
	h := NewPageHandler(rig.db, nil, newTestLogger())
	heads := []struct {
		page, contentType string
		latest, shared    int
	}{
		{"lib-project", "artifact_project", 4, 2},
		{"lib-word", "wordcraft", 5, 0},
		{"lib-sheet", "gridcraft", 3, 3},
	}
	rig.seedPage(t, "lib-plain", "lib-plain", "lib-plain", rig.userID, "")
	for _, head := range heads {
		rig.seedPage(t, head.page, head.page, head.page, rig.userID, "")
		execOrFatal(t, h.db, `INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES(?,?,?,?,?)`, rig.wsID, head.page, head.contentType, head.latest, head.shared)
	}
	rows := pagesListRows(t, h, rig.wsID, rig.userID, "MEMBER")
	want := map[string]struct {
		kind   any
		shared any
	}{
		"lib-project": {"project", float64(2)},
		"lib-word":    {"word", nil},
		"lib-sheet":   {"sheet", float64(3)},
		"lib-plain":   {nil, nil},
	}
	for page, w := range want {
		row := rows[page]
		if row == nil {
			t.Fatalf("%s missing from the list", page)
		}
		if row["artifact_kind"] != w.kind || row["artifact_shared_version"] != w.shared {
			t.Errorf("%s = (%v,%v), want (%v,%v)", page, row["artifact_kind"], row["artifact_shared_version"], w.kind, w.shared)
		}
		for key := range row {
			if strings.Contains(key, "latest") {
				t.Errorf("%s exposes %q in the list; the latest version is not a reader's fact", page, key)
			}
		}
	}
}
