package kinds

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// fileServingFake answers the crew-file download endpoint from a map keyed
// by "shared/..." path; any other path falls through to crewFakeClient.
type fileServingFake struct {
	*crewFakeClient
	files map[string]string
}

func (f *fileServingFake) Get(ctx context.Context, path string) (*internalapi.Response, error) {
	if i := strings.Index(path, "/files/download?path="); i >= 0 {
		p, _ := url.QueryUnescape(path[i+len("/files/download?path="):])
		if body, ok := f.files[p]; ok {
			return &internalapi.Response{StatusCode: 200, Body: strings.NewReader(body)}, nil
		}
		return crewJSONResp(404, map[string]any{"error": "File not found"}), nil
	}
	return f.crewFakeClient.Get(ctx, path)
}

// #5334: a steady-state apply must converge. A declared file the crew
// already holds byte-for-byte plans as unchanged with no write; a changed
// byte, a missing file or an unreadable one keeps the write.
func TestCrewPlanFiles_UnchangedContentIsNoop(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"same.sh": "echo same\n", "changed.sh": "echo new\n", "missing.sh": "echo x\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	doc := makeCrewDoc()
	doc.Spec.Files = []CrewFile{
		{Src: "same.sh", Dest: "shared/same.sh"},
		{Src: "changed.sh", Dest: "shared/changed.sh"},
		{Src: "missing.sh", Dest: "shared/missing.sh"},
	}
	c := &fileServingFake{crewFakeClient: newCrewFake(), files: map[string]string{
		"shared/same.sh":    "echo same\n",
		"shared/changed.sh": "echo old\n",
	}}

	items, err := doc.planCrewFiles(context.Background(), c, "crew_1", dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]internalapi.PlanAction{
		"shared/same.sh":    internalapi.ActionUnchanged,
		"shared/changed.sh": internalapi.ActionUpdate,
		"shared/missing.sh": internalapi.ActionUpdate,
	}
	if len(items) != len(want) {
		t.Fatalf("items = %d, want %d: %+v", len(items), len(want), items)
	}
	for _, it := range items {
		dest := it.Slug[strings.Index(it.Slug, "/")+1:]
		if it.Action != want[dest] {
			t.Errorf("%s: action %v, want %v", dest, it.Action, want[dest])
		}
		if it.Action == internalapi.ActionUnchanged && it.Exec != nil {
			t.Errorf("%s: unchanged item must not write", dest)
		}
		if it.Action != internalapi.ActionUnchanged && it.Exec == nil {
			t.Errorf("%s: changed item has no write", dest)
		}
	}

	// A new crew has nothing to compare against: every file is a create
	// and the server is not asked.
	fresh := &fileServingFake{crewFakeClient: newCrewFake(), files: c.files}
	items, err = doc.planCrewFiles(context.Background(), fresh, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Action != internalapi.ActionCreate {
			t.Errorf("new crew %s: action %v, want create", it.Slug, it.Action)
		}
	}
}
