//go:build linux

package restricteddispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestNativeExplicitProjectInputsSeparateHumansAndRevokePreparedPrompt(t *testing.T) {
	a := nativeFixture(t)
	if _, err := a.Store.DB.ExecContext(t.Context(), `INSERT INTO projects(id,workspace_id,name,slug) VALUES('p1','w','P1','p1'),('p2','w','P2','p2')`); err != nil {
		t.Fatal(err)
	}
	one, err := a.Store.PutProjectFile(t.Context(), "owner", "w", "p1", access.ProjectFileWrite{Name: "H1_FILE_NAME.txt"}, []byte("H1_PROJECT_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.Store.PutProjectFile(t.Context(), "owner", "w", "p2", access.ProjectFileWrite{Name: "H2_FILE_NAME.txt"}, []byte("H2_PROJECT_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	setRights(t, a, "h1", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "project", ID: "p1", Operation: "read"}})
	setRights(t, a, "h2", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "project", ID: "p2", Operation: "read"}})
	builds := 0
	build := func(ctx context.Context, attempt access.Attempt) (NativePrompt, error) {
		builds++
		p, err := a.Store.BuildContext(ctx, attempt, "own task")
		return NativePrompt{Instructions: p.System, Input: p.Input}, err
	}
	h1, first, err := a.prepareNativeProjectFiles(t.Context(), "h1", "w", "a", "c1", "", nil, 128, []string{one.ID}, build, false)
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := a.prepareNativeProjectFiles(t.Context(), "h2", "w", "a", "c2", "", nil, 128, []string{two.ID}, build, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.prepareNativeProjectFiles(t.Context(), "h2", "w", "a", "c2", "", nil, 128, []string{one.ID}, build, false); err == nil || builds != 2 {
		t.Fatal("foreign source reached admission/context builder")
	}
	p1, err := a.Resolve(t.Context(), h1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := a.Resolve(t.Context(), h2)
	if err != nil {
		t.Fatal(err)
	}
	if p1.NativeInputs == nil || p2.NativeInputs == nil || len(p1.Mounts) != 1 || !p1.Mounts[0].ReadOnly || p1.Mounts[0].Resource == p2.Mounts[0].Resource || p1.NativeInputs.Files[0].VersionID != one.ID || p2.NativeInputs.Files[0].VersionID != two.ID {
		t.Fatal("source selection widened or aliased another human")
	}
	source := ProjectInputSource(a.Store)
	data, err := source(t.Context(), p1)
	if err != nil || len(data) != 1 || string(data[0].Content) != "H1_PROJECT_CANARY" {
		t.Fatal("wrong frozen source", err)
	}
	var instructions, input string
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT instructions,initial_input FROM restricted_native_sessions WHERE attempt_id=?`, first.ID).Scan(&instructions, &input); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(instructions, one.Name) || strings.Contains(instructions, "H1_PROJECT_CANARY") || !strings.Contains(input, one.ID) || strings.Contains(input, two.ID) || strings.Contains(input, "H2_PROJECT_CANARY") {
		t.Fatal("project data laundered into instructions or foreign context")
	}
	if err := a.Store.RetireProjectFile(t.Context(), "owner", "w", "p1", one.FileID, one.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Resolve(t.Context(), h1); err == nil {
		t.Fatal("retired source retained prepared runtime authority")
	}
	if _, err := source(t.Context(), p1); err == nil {
		t.Fatal("retired source still materialized")
	}
	if _, _, err := a.NativeRequest(t.Context(), h1, "key", nativeRaw(t)); err == nil {
		t.Fatal("retired source reached provider request")
	}
	data, err = source(t.Context(), p2)
	if err != nil || len(data) != 1 || string(data[0].Content) != "H2_PROJECT_CANARY" {
		t.Fatal("other human's valid source was suppressed", err)
	}
}
