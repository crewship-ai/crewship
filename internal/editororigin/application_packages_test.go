package editororigin

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

type applicationBudgetFS struct {
	fs.FS
	opened, closed, read int
	fail                 bool
}
type applicationBudgetFile struct {
	fs.File
	owner *applicationBudgetFS
}

func (s *applicationBudgetFS) Open(name string) (fs.File, error) {
	f, e := s.FS.Open(name)
	if e != nil {
		return nil, e
	}
	if strings.HasSuffix(name, "/manifest.json") {
		s.opened++
		return &applicationBudgetFile{f, s}, nil
	}
	return f, nil
}
func (f *applicationBudgetFile) Read(p []byte) (int, error) {
	if f.owner.fail {
		return 0, errors.New("controlled reader failure")
	}
	for i := range p {
		p[i] = 'x'
	}
	f.owner.read += len(p)
	return len(p), nil
}
func (f *applicationBudgetFile) Close() error { f.owner.closed++; return f.File.Close() }
func TestApplicationPackageReadBudgetAndErrorClose(t *testing.T) {
	pin := strings.Repeat("1", 64)
	for _, fail := range []bool{false, true} {
		source := &applicationBudgetFS{FS: fstest.MapFS{pin + "/manifest.json": &fstest.MapFile{Data: []byte("x")}}, fail: fail}
		h, err := New(Options{SameHost: true, StudioOrigin: studio, ApplicationPackages: source})
		if err == nil || h.HasBundle(pin) {
			t.Fatal("unbounded/failing package accepted")
		}
		if source.opened != 1 || source.closed != 1 {
			t.Fatalf("reader leaked: open=%d close=%d", source.opened, source.closed)
		}
		if source.read > maxManifestBytes+1 {
			t.Fatalf("manifest read past budget: %d", source.read)
		}
		if !fail && source.read != maxManifestBytes+1 {
			t.Fatalf("missing actual bounded-read exercise: %d", source.read)
		}
	}
}
func TestApplicationPackageRootNeverFallsBackToCompiledEditor(t *testing.T) {
	for _, source := range []fs.FS{fstest.MapFS{}, fstest.MapFS{"../bad/manifest.json": &fstest.MapFile{Data: []byte("x")}}} {
		h, _ := New(Options{SameHost: true, StudioOrigin: studio, ApplicationPackages: source})
		if d, u := h.PinnedBundle(sessionReq("studio.example.test"), strings.Repeat("2", 64)); d != "" || u != "" {
			t.Fatal("missing requested application fell back")
		}
		if d, u := h.SessionBundle(sessionReq("studio.example.test")); d != "" || u != "" {
			t.Fatal("compiled packages silently selected application")
		}
	}
}
