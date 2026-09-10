package backup

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

func TestPageRestoreRejectsMalformedPointersWithoutPanicking(t *testing.T) {
	for _, value := range []any{nil, 42, "", "bad"} {
		dump := &DBDump{Tables: map[string][]map[string]any{"page_project_revisions": {{"page_id": "page", "git_commit": strings.Repeat("a", 40), "spec_json": "{}", "source_digest": value}}}}
		if _, err := collectPageFileRefs(dump); err == nil {
			t.Fatalf("accepted malformed digest %#v", value)
		}
	}
}

func TestPageRestoreValidationDoesNotWriteTarget(t *testing.T) {
	source := pageprofile.Source()
	raw, err := source.MarshalYAMLSource()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := source.Digest()
	if err != nil {
		t.Fatal(err)
	}
	dump := &DBDump{WorkspaceID: "workspace", Tables: map[string][]map[string]any{
		"workspaces": {{"id": "workspace"}}, "pages": {{"id": "page"}},
		"page_project_drafts": {{"page_id": "page", "source_digest": digest}},
	}}
	type entry struct {
		name string
		data []byte
		kind byte
	}
	for _, test := range []struct {
		name    string
		entries []entry
		valid   bool
	}{
		{"missing", nil, false},
		{"corrupt", []entry{{"sources/" + digest + ".yaml", []byte("not a source"), tar.TypeReg}}, false},
		{"path escape", []entry{{"../sources/" + digest + ".yaml", raw, tar.TypeReg}}, false},
		{"link", []entry{{"sources/" + digest + ".yaml", nil, tar.TypeSymlink}}, false},
		{"duplicate", []entry{{"sources/" + digest + ".yaml", raw, tar.TypeReg}, {"sources/" + digest + ".yaml", raw, tar.TypeReg}}, false},
		{"valid dry preparation", []entry{{"sources/" + digest + ".yaml", raw, tar.TypeReg}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "files.tar")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			writer := tar.NewWriter(f)
			for _, entry := range test.entries {
				if err := writer.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.data)), Mode: 0600, Typeflag: entry.kind}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(entry.data); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			f.Close()
			target := filepath.Join(t.TempDir(), "target")
			apply, cleanup, err := preparePageProjectsRestore(context.Background(), &ExtractedPayload{pageProjectsPath: path}, target, dump, []string{"page"})
			if cleanup != nil {
				defer cleanup()
			}
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && err == nil {
				t.Fatal("invalid archive accepted")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("preparation changed target: %v", err)
			}
			if test.valid {
				if err := apply(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, err := (&pages.ProjectStore{Directory: target}).Get(context.Background(), "workspace", digest); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPageRestoreRejectsMissingExtraAndCorruptGitObjects(t *testing.T) {
	ctx := context.Background()
	source := pageprofile.Source()
	store := &pages.ProjectStore{Directory: t.TempDir()}
	digest, err := store.Put(ctx, "workspace", source)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := store.Checkpoint(ctx, "workspace", "page", "", "{}", "test", 1, source)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := source.MarshalYAMLSource()
	if err != nil {
		t.Fatal(err)
	}
	type object struct {
		id, kind string
		data     []byte
	}
	var objects []object
	if err := store.VisitGitObjects(ctx, "workspace", "page", []string{commit}, func(id, kind string, data []byte) error {
		objects = append(objects, object{id, kind, data})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dump := &DBDump{WorkspaceID: "workspace", Tables: map[string][]map[string]any{
		"workspaces": {{"id": "workspace"}}, "pages": {{"id": "page"}},
		"page_project_revisions": {{"page_id": "page", "source_digest": digest, "git_commit": commit, "spec_json": "{}"}},
	}}
	for _, mode := range []string{"missing", "extra", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "files.tar")
			f, err := os.Create(file)
			if err != nil {
				t.Fatal(err)
			}
			writer := tar.NewWriter(f)
			write := func(name string, data []byte) {
				t.Helper()
				if err := writer.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0600, Typeflag: tar.TypeReg}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			write("sources/"+digest+".yaml", raw)
			for i, obj := range objects {
				if mode == "missing" && i == len(objects)-1 {
					continue
				}
				data := obj.data
				if mode == "corrupt" && i == 0 {
					data = append(append([]byte{}, data...), byte('!'))
				}
				write("git/"+pageArchiveKey("page")+"/"+obj.kind+"/"+obj.id, data)
			}
			if mode == "extra" {
				// SHA-1 of Git's canonical empty blob, valid but not reachable from SQL.
				write("git/"+pageArchiveKey("page")+"/blob/e69de29bb2d1d6434b8b29ae775ad8c2e48c5391", nil)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			f.Close()
			target := filepath.Join(t.TempDir(), "target")
			_, cleanup, err := preparePageProjectsRestore(ctx, &ExtractedPayload{pageProjectsPath: file}, target, dump, []string{"page"})
			if cleanup != nil {
				defer cleanup()
			}
			if err == nil {
				t.Fatal("invalid Git archive accepted")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("invalid archive touched target")
			}
		})
	}
}
