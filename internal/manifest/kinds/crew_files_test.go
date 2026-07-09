package kinds

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// putBytesRecorder extends crewFakeClient with the raw-bytes PUT capability
// crew-file delivery needs, capturing each save so the test can assert the
// destination path and payload the standalone Crew apply produced.
type putBytesRecorder struct {
	*crewFakeClient
	saves map[string][]byte
}

func newPutBytesRecorder() *putBytesRecorder {
	return &putBytesRecorder{crewFakeClient: newCrewFake(), saves: map[string][]byte{}}
}

func (r *putBytesRecorder) PutBytes(_ context.Context, path string, data []byte) (*internalapi.Response, error) {
	r.record("PUTBYTES", path)
	cp := make([]byte, len(data))
	copy(cp, data)
	r.saves[path] = cp
	return crewJSONResp(200, map[string]any{"status": "saved"}), nil
}

// TestCrewPlan_StandaloneDeliversFiles is the #921 regression: a standalone
// kind:Crew document with a spec.files block must emit crew-file plan items
// and materialize them at apply — the SPEC-2 path historically dropped the
// files block silently, so only the combined manifest delivered them.
func TestCrewPlan_StandaloneDeliversFiles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "parse_check.sh")
	if err := os.WriteFile(src, []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	doc := makeCrewDoc()
	doc.Metadata.Slug = "scriptval"
	doc.Spec.Files = []CrewFile{
		{Src: "parse_check.sh", Dest: "shared/scripts/parse_check.sh"},
	}

	items, err := doc.Plan(context.Background(), newCrewFake(), nil, PlanCrewOptions{BaseDir: dir})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Expect a crew item + a crew-file item.
	var crewItem, fileItem *internalapi.PlanItem
	for i := range items {
		switch items[i].Kind {
		case "crew":
			crewItem = &items[i]
		case "crew-file":
			fileItem = &items[i]
		}
	}
	if fileItem == nil {
		t.Fatalf("standalone Crew with files: produced no crew-file plan item; items=%+v", items)
	}

	// Apply in order: the crew-create item materializes the crew, then the
	// file item resolves it by slug and delivers the bytes via raw PUT —
	// exactly how the apply loop walks the plan.
	rec := newPutBytesRecorder()
	if err := crewItem.Exec(context.Background(), rec); err != nil {
		t.Fatalf("exec crew create: %v", err)
	}
	if err := fileItem.Exec(context.Background(), rec); err != nil {
		t.Fatalf("exec crew-file: %v", err)
	}
	found := false
	for path, data := range rec.saves {
		if strings.Contains(path, "crew_scriptval") &&
			strings.Contains(path, "parse_check.sh") && string(data) == "echo hi\n" {
			found = true
		}
	}
	if !found {
		t.Fatalf("crew-file exec did not PUT the expected bytes to the crew; saves=%v", rec.saves)
	}
}

// TestCrewValidate_RejectsBadFileDest keeps the shared-tree fence: a files
// entry that escapes shared/ must fail validation, same as the legacy path.
func TestCrewValidate_RejectsBadFileDest(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Files = []CrewFile{{Src: "x.sh", Dest: "../../etc/passwd"}}
	if err := doc.Validate(internalapi.WorkspaceContext{}); err == nil {
		t.Fatal("files entry escaping shared/ must be rejected at validate")
	}
}
