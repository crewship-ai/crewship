package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeCrewFileIO is an in-memory crewFileIO for testing inline/materialize
// without a live server. keyed by "crewID\x00crewPath".
type fakeCrewFileIO struct {
	files    map[string][]byte
	saved    map[string][]byte
	saveErr  error
	dlErr    error
	saveHits int
}

func newFakeCrewFileIO() *fakeCrewFileIO {
	return &fakeCrewFileIO{files: map[string][]byte{}, saved: map[string][]byte{}}
}

func fkey(crewID, crewPath string) string { return crewID + "\x00" + crewPath }

func (f *fakeCrewFileIO) download(crewID, crewPath string) ([]byte, bool, error) {
	if f.dlErr != nil {
		return nil, false, f.dlErr
	}
	b, ok := f.files[fkey(crewID, crewPath)]
	return b, ok, nil
}

func (f *fakeCrewFileIO) save(crewID, crewPath string, data []byte) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saveHits++
	f.saved[fkey(crewID, crewPath)] = data
	f.files[fkey(crewID, crewPath)] = data
	return nil
}

// ---- collectScriptPaths ----

func TestCollectScriptPaths(t *testing.T) {
	def := []byte(`{
	  "dsl_version": "1.0",
	  "name": "acct",
	  "steps": [
	    {"id": "parse", "type": "script", "script": {"path": "scripts/parse_vypis.py", "interpreter": "python3"}},
	    {"id": "verify", "type": "script", "script": {"path": "/crew/shared/scripts/verify.py"}},
	    {"id": "dup", "type": "script", "script": {"path": "scripts/parse_vypis.py"}},
	    {"id": "summ", "type": "agent_run", "agent_slug": "acct", "prompt": "go"}
	  ]
	}`)
	got, err := collectScriptPaths(def)
	if err != nil {
		t.Fatalf("collectScriptPaths: %v", err)
	}
	// unique + sorted; the agent_run step contributes nothing.
	want := []string{"/crew/shared/scripts/verify.py", "scripts/parse_vypis.py"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func TestCollectScriptPaths_NoScriptSteps(t *testing.T) {
	def := []byte(`{"dsl_version":"1.0","name":"x","steps":[{"id":"a","type":"agent_run","agent_slug":"a","prompt":"p"}]}`)
	got, err := collectScriptPaths(def)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no script paths, got %v", got)
	}
}

// ---- scriptCrewFilePath ----

func TestScriptCrewFilePath(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"relative", "scripts/parse.py", "shared/scripts/parse.py", true},
		{"absolute under shared", "/crew/shared/scripts/verify.py", "shared/scripts/verify.py", true},
		// A "shared/"-prefixed RELATIVE path is NOT special-cased: the runner
		// (internal/pipeline/runner_script.go resolveScriptPath) prepends
		// /crew/shared/ to every relative path, so it execs
		// /crew/shared/shared/scripts/x.py. We must map to the SAME location or
		// export/import would touch a file the routine never runs.
		{"shared-prefixed relative", "shared/scripts/x.py", "shared/shared/scripts/x.py", true},
		{"relative traversal escapes shared", "../../etc/passwd", "", false},
		{"absolute path outside shared", "/etc/passwd", "", false},
		{"absolute under /crew but not shared", "/crew/output/x.py", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := scriptCrewFilePath(c.in)
			switch {
			case c.ok && err != nil:
				t.Fatalf("scriptCrewFilePath(%q) unexpected err: %v", c.in, err)
			case !c.ok && err == nil:
				t.Fatalf("scriptCrewFilePath(%q) = %q, want error", c.in, got)
			case c.ok && got != c.want:
				t.Fatalf("scriptCrewFilePath(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// ---- inlineScripts ----

func TestInlineScripts(t *testing.T) {
	def := []byte(`{"dsl_version":"1.0","name":"a","steps":[
	  {"id":"p","type":"script","script":{"path":"scripts/parse.py"}},
	  {"id":"s","type":"agent_run","agent_slug":"a","prompt":"x"}]}`)
	io := newFakeCrewFileIO()
	io.files[fkey("crew_1", "shared/scripts/parse.py")] = []byte("print('hi')\n")

	entries, err := inlineScripts(io, "crew_1", def)
	if err != nil {
		t.Fatalf("inlineScripts: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Path != "scripts/parse.py" {
		t.Errorf("path = %q", entries[0].Path)
	}
	// content_b64 must decode back to the original bytes.
	if entries[0].ContentB64 == "" {
		t.Errorf("content_b64 empty")
	}
}

func TestInlineScripts_MissingFileErrors(t *testing.T) {
	def := []byte(`{"dsl_version":"1.0","name":"a","steps":[{"id":"p","type":"script","script":{"path":"scripts/missing.py"}}]}`)
	io := newFakeCrewFileIO()
	_, err := inlineScripts(io, "crew_1", def)
	if err == nil {
		t.Fatal("expected error for a script step whose file is absent from the author crew")
	}
}

// ---- materializeScripts (collision policy) ----

func TestMaterializeScripts_NewFileSaves(t *testing.T) {
	io := newFakeCrewFileIO()
	scripts := []scriptEntry{{Path: "scripts/parse.py", ContentB64: b64("print(1)")}}
	if err := materializeScripts(io, "crew_2", scripts, false); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if io.saveHits != 1 {
		t.Fatalf("saveHits = %d, want 1", io.saveHits)
	}
	if string(io.saved[fkey("crew_2", "shared/scripts/parse.py")]) != "print(1)" {
		t.Errorf("saved content mismatch")
	}
}

func TestMaterializeScripts_IdenticalIsIdempotentSkip(t *testing.T) {
	io := newFakeCrewFileIO()
	io.files[fkey("crew_2", "shared/scripts/parse.py")] = []byte("print(1)")
	scripts := []scriptEntry{{Path: "scripts/parse.py", ContentB64: b64("print(1)")}}
	if err := materializeScripts(io, "crew_2", scripts, false); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if io.saveHits != 0 {
		t.Fatalf("identical content must skip the write, saveHits = %d", io.saveHits)
	}
}

func TestMaterializeScripts_ConflictFailsLoudlyWithoutForce(t *testing.T) {
	io := newFakeCrewFileIO()
	io.files[fkey("crew_2", "shared/scripts/parse.py")] = []byte("OLD")
	scripts := []scriptEntry{{Path: "scripts/parse.py", ContentB64: b64("NEW")}}
	err := materializeScripts(io, "crew_2", scripts, false)
	if err == nil {
		t.Fatal("expected a loud conflict error when dest differs and --force is off")
	}
	if !strings.Contains(err.Error(), "force") {
		t.Errorf("conflict error should mention --force, got: %v", err)
	}
	if io.saveHits != 0 {
		t.Errorf("must not overwrite without --force")
	}
}

func TestMaterializeScripts_ConflictForceOverwrites(t *testing.T) {
	io := newFakeCrewFileIO()
	io.files[fkey("crew_2", "shared/scripts/parse.py")] = []byte("OLD")
	scripts := []scriptEntry{{Path: "scripts/parse.py", ContentB64: b64("NEW")}}
	if err := materializeScripts(io, "crew_2", scripts, true); err != nil {
		t.Fatalf("materialize --force: %v", err)
	}
	if string(io.saved[fkey("crew_2", "shared/scripts/parse.py")]) != "NEW" {
		t.Errorf("force did not overwrite")
	}
}

func TestMaterializeScripts_BadBase64Errors(t *testing.T) {
	io := newFakeCrewFileIO()
	scripts := []scriptEntry{{Path: "scripts/x.py", ContentB64: "!!!not base64!!!"}}
	if err := materializeScripts(io, "crew_2", scripts, false); err == nil {
		t.Fatal("expected error on undecodable content_b64")
	}
}

// TestExportImportRoundtrip proves the moat scenario: a script inlined from an
// author crew survives serialization into a bundle and re-materializes,
// byte-identical, into a FRESH crew that never had it.
func TestExportImportRoundtrip(t *testing.T) {
	def := []byte(`{"dsl_version":"1.0","name":"acct","steps":[
	  {"id":"parse","type":"script","script":{"path":"scripts/parse.py","interpreter":"python3"}},
	  {"id":"reconcile","type":"agent_run","agent_slug":"acct","prompt":"reconcile {{ steps.parse.output }}"}]}`)
	src := newFakeCrewFileIO()
	original := []byte("import sys; print('checksum ok')\n")
	src.files[fkey("crew_src", "shared/scripts/parse.py")] = original

	// export: inline from the source crew.
	entries, err := inlineScripts(src, "crew_src", def)
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	// serialize into a bundle map, as the export command does.
	bundle := map[string]any{"scripts": entries}
	wire, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// import side: decode from the wire bundle and materialize into a FRESH crew.
	var decoded map[string]any
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, err := decodeBundleScripts(decoded)
	if err != nil {
		t.Fatalf("decodeBundleScripts: %v", err)
	}
	dst := newFakeCrewFileIO()
	if err := materializeScripts(dst, "crew_fresh", got, false); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	landed := dst.saved[fkey("crew_fresh", "shared/scripts/parse.py")]
	if string(landed) != string(original) {
		t.Fatalf("roundtrip mismatch: landed %q, want %q", landed, original)
	}
}

func TestDecodeBundleScripts_AbsentIsNil(t *testing.T) {
	got, err := decodeBundleScripts(map[string]any{"pipeline": map[string]any{}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for a bundle with no scripts, got %v", got)
	}
}

func TestMaterializeScripts_SaveErrorPropagates(t *testing.T) {
	io := newFakeCrewFileIO()
	io.saveErr = errors.New("boom")
	scripts := []scriptEntry{{Path: "scripts/x.py", ContentB64: b64("data")}}
	if err := materializeScripts(io, "crew_2", scripts, false); err == nil {
		t.Fatal("expected save error to propagate")
	}
}
