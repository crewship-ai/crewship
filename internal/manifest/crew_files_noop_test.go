package manifest

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #5334: the combined-manifest planner converges like the kind path. A file
// the crew already holds byte-for-byte is unchanged with no write; a changed,
// missing or unreadable one keeps the write; only files still to be written
// count toward the stopped-crew advisory.
func TestPlanCrewFiles_UnchangedContentIsNoop(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"same.sh": "echo same\n", "changed.sh": "echo new\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []CrewFile{{Src: "same.sh", Dest: "shared/same.sh"}, {Src: "changed.sh", Dest: "shared/changed.sh"}}
	remote := map[string]string{"shared/same.sh": "echo same\n", "shared/changed.sh": "echo old\n"}

	newPB := func(stored map[string]string) *planBuilder {
		pb := &planBuilder{plan: &Plan{}, client: &Client{}, opts: Options{BaseDir: dir}}
		pb.containerStatusFn = func(string) (bool, bool) { return false, true } // stopped
		pb.crewFileContentFn = func(_, path string) ([]byte, bool) {
			b, ok := stored[path]
			return []byte(b), ok
		}
		return pb
	}

	pb := newPB(remote)
	if err := pb.planCrewFiles(context.Background(), "ops", "crew_1", files); err != nil {
		t.Fatal(err)
	}
	got := map[string]Action{}
	for _, it := range pb.plan.Items {
		got[it.Description] = it.Action
	}
	if got["ops/shared/same.sh"] != ActionUnchanged || got["ops/shared/changed.sh"] != ActionUpdate {
		t.Fatalf("actions = %v, want same.sh unchanged and changed.sh update", got)
	}
	if len(pb.plan.Warnings) != 1 || !strings.Contains(pb.plan.Warnings[0], "writes 1 file(s)") {
		t.Fatalf("warnings = %v, want one advisory counting only the file to write", pb.plan.Warnings)
	}

	// Everything already in place: no write and no stopped-crew advisory.
	pb = newPB(map[string]string{"shared/same.sh": "echo same\n", "shared/changed.sh": "echo new\n"})
	if err := pb.planCrewFiles(context.Background(), "ops", "crew_1", files); err != nil {
		t.Fatal(err)
	}
	for _, it := range pb.plan.Items {
		if it.Action != ActionUnchanged {
			t.Fatalf("%s: action %v, want unchanged", it.Description, it.Action)
		}
	}
	if len(pb.plan.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none when nothing is written", pb.plan.Warnings)
	}
}

type downloadAPI struct {
	APIClient
	status int
	body   string
	err    error
	path   string
}

func (d *downloadAPI) Get(_ context.Context, path string) (*http.Response, error) {
	d.path = path
	if d.err != nil {
		return nil, d.err
	}
	return &http.Response{StatusCode: d.status, Body: io.NopCloser(strings.NewReader(d.body))}, nil
}

func TestClientCrewFileContent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		api    *downloadAPI
		wantOK bool
	}{
		{"ok", &downloadAPI{status: 200, body: "abc"}, true},
		{"missing", &downloadAPI{status: 404, body: `{"error":"File not found"}`}, false},
		{"stopped or unreadable", &downloadAPI{status: 502, body: "x"}, false},
		{"transport error", &downloadAPI{err: io.ErrUnexpectedEOF}, false},
		{"larger than a manifest file", &downloadAPI{status: 200, body: strings.Repeat("a", crewFileMaxBytes+1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, ok := NewClient(tc.api).CrewFileContent(context.Background(), "c 1", "shared/a b.sh")
			if ok != tc.wantOK || (ok && string(data) != tc.api.body) {
				t.Fatalf("got (%q, %v), want ok=%v", data, ok, tc.wantOK)
			}
			if want := "/api/v1/crews/c%201/files/download?path=shared%2Fa+b.sh"; tc.api.path != want {
				t.Fatalf("path = %q, want %q", tc.api.path, want)
			}
		})
	}
}
