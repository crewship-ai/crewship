package apple

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/safepath"
)

// tarWith renders entries into an archive. size overrides the declared
// header size when non-zero, so a test can claim a huge entry without
// materialising it.
type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
	declared int64
}

func tarWith(t *testing.T, entries ...tarEntry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		size := int64(len(e.body))
		if e.declared != 0 {
			size = e.declared
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     e.name,
			Mode:     0o600,
			Size:     size,
			Typeflag: flag,
			Linkname: e.linkname,
		}); err != nil {
			t.Fatalf("writing header %q: %v", e.name, err)
		}
		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("writing body %q: %v", e.name, err)
			}
			// Pad to the declared size so the archive stays well-formed.
			if pad := size - int64(len(e.body)); pad > 0 {
				if _, err := tw.Write(make([]byte, pad)); err != nil {
					t.Fatalf("padding %q: %v", e.name, err)
				}
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing archive: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

// TestUnpackTarIntoRefusesEscape is the test the boundary comment on
// unpackTarInto always claimed. Each vector must be refused *and* must
// leave nothing behind outside the staging root — a rejection that still
// wrote the file is not a rejection.
func TestUnpackTarIntoRefusesEscape(t *testing.T) {
	vectors := []struct {
		name  string
		entry string
	}{
		{"parent traversal", "../escaped"},
		{"nested traversal", "a/../../escaped"},
		{"absolute path", "/etc/escaped"},
		{"deep traversal", "../../../escaped"},
		// filepath.Clean does not treat a backslash as a separator on
		// unix, so a hand-rolled path.Clean check waves this through and
		// it escapes once the same archive is unpacked on Windows.
		{"backslash traversal", `..\escaped`},
		{"backslash nested", `a\..\..\escaped`},
	}

	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			outer := t.TempDir()
			root := filepath.Join(outer, "staging")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatalf("mkdir root: %v", err)
			}

			names, err := unpackTarInto(root, tarWith(t, tarEntry{name: v.entry, body: "pwned"}))
			if err == nil {
				t.Fatalf("accepted escaping entry %q (returned names %v); want refusal", v.entry, names)
			}
			if !errors.Is(err, safepath.ErrUnsafe) {
				t.Errorf("error = %v; want one wrapping safepath.ErrUnsafe", err)
			}

			// Nothing may exist anywhere under the parent of the root
			// except the (empty) root itself.
			var strays []string
			if walkErr := filepath.WalkDir(outer, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if p == outer || p == root {
					return nil
				}
				strays = append(strays, strings.TrimPrefix(p, outer))
				return nil
			}); walkErr != nil {
				t.Fatalf("walking %s: %v", outer, walkErr)
			}
			if len(strays) > 0 {
				t.Errorf("refused entry %q but left %v on disk", v.entry, strays)
			}
		})
	}
}

// TestUnpackTarIntoRefusesTarBomb pins the cumulative cap. A per-entry
// limit alone does not bound an archive: many entries just under the cap
// still fill the host disk (the lesson devcontainer/features.go records
// as Audit M24).
func TestUnpackTarIntoRefusesTarBomb(t *testing.T) {
	root := t.TempDir()

	// Injected caps, not the production ones. A tar writer materialises
	// every declared byte, so proving a 256 MB limit at its real value means
	// allocating more than that to build the fixture — the first version of
	// this test peaked at 3.7 GB, passed on a workstation, and killed the
	// arm64 CI jobs, which compile several packages at once. The ratio is
	// what is under test, not the constants.
	const (
		testEntryCap = 1 << 10
		testTotalCap = 4 << 10
	)

	// Each entry declares just under the per-entry cap; together they
	// blow past the total.
	var entries []tarEntry
	for i := 0; i < 40; i++ {
		entries = append(entries, tarEntry{
			name:     filepath.Join("bomb", string(rune('a'+i%26))+string(rune('a'+i/26))),
			body:     "x",
			declared: testEntryCap - 1,
		})
	}

	if _, err := unpackTarIntoLimited(root, tarWith(t, entries...), testEntryCap, testTotalCap); err == nil {
		t.Fatal("accepted an archive declaring far past the cumulative cap; want refusal")
	} else if !strings.Contains(err.Error(), "cumulative") {
		t.Errorf("error = %v; want it to name the cumulative cap", err)
	}
}

// TestUnpackTarIntoSkipsNonRegular keeps links out of the staging tree —
// a symlink entry can redirect a later in-bounds write outside the root.
func TestUnpackTarIntoSkipsNonRegular(t *testing.T) {
	root := t.TempDir()

	names, err := unpackTarInto(root, tarWith(t,
		tarEntry{name: "link", typeflag: tar.TypeSymlink, linkname: "../../etc/passwd"},
		tarEntry{name: "hard", typeflag: tar.TypeLink, linkname: "../../etc/passwd"},
		tarEntry{name: "real.json", body: "{}"},
	))
	if err != nil {
		t.Fatalf("unpackTarInto: %v", err)
	}
	if len(names) != 1 || names[0] != "real.json" {
		t.Fatalf("names = %v; want only [real.json]", names)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Errorf("symlink entry was materialised (lstat err = %v)", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "hard")); !os.IsNotExist(err) {
		t.Errorf("hardlink entry was materialised (lstat err = %v)", err)
	}
}

// TestUnpackTarIntoWritesNestedFiles is the happy path: a legitimate
// nested tree lands under the root with its contents intact.
func TestUnpackTarIntoWritesNestedFiles(t *testing.T) {
	root := t.TempDir()

	// "./" and "//" are normalisations, not escapes — GNU tar emits the
	// leading "./" routinely, so refusing them would reject legitimate
	// archives. They must land in-bounds under their cleaned name.
	names, err := unpackTarInto(root, tarWith(t,
		tarEntry{name: "devcontainer.json", body: `{"image":"x"}`},
		tarEntry{name: "features/node/install.sh", body: "#!/bin/sh\n"},
		tarEntry{name: "./dotted.json", body: "{}"},
		tarEntry{name: "double//slash.json", body: "{}"},
	))
	if err != nil {
		t.Fatalf("unpackTarInto: %v", err)
	}
	if len(names) != 4 {
		t.Fatalf("names = %v; want 4 entries", names)
	}
	for _, want := range []string{"dotted.json", filepath.Join("double", "slash.json")} {
		if _, err := os.Stat(filepath.Join(root, want)); err != nil {
			t.Errorf("normalised entry %q not written: %v", want, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(root, "features", "node", "install.sh"))
	if err != nil {
		t.Fatalf("reading nested file: %v", err)
	}
	if string(got) != "#!/bin/sh\n" {
		t.Errorf("nested file = %q; want the shebang body", got)
	}
}

// TestUnpackTarIntoRefusesSymlinkedRoot is the filesystem layer the
// lexical check cannot reach: if a component inside the staging tree is
// a symlink pointing out, an entry that is textually in-bounds still
// writes outside. os.Root refuses to follow it.
func TestUnpackTarIntoRefusesSymlinkedRoot(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "staging")
	victim := filepath.Join(outer, "victim")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	// A pre-existing symlink inside the staging tree, as a hostile local
	// process could plant between staging and unpack.
	if err := os.Symlink(victim, filepath.Join(root, "out")); err != nil {
		t.Fatalf("creating the symlink fixture: %v", err)
	}

	if _, err := unpackTarInto(root, tarWith(t, tarEntry{name: "out/escaped", body: "pwned"})); err == nil {
		t.Fatal("wrote through a symlinked directory; want refusal")
	}
	if _, err := os.Stat(filepath.Join(victim, "escaped")); !os.IsNotExist(err) {
		t.Errorf("write landed in the victim directory (stat err = %v)", err)
	}
}
