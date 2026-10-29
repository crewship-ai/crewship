package pages

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func projectCompressionFixtures() [][]byte {
	random := make([]byte, 96<<10)
	_, _ = rand.New(rand.NewSource(781)).Read(random)
	return [][]byte{nil, []byte("a"), {0, 255, 0, 17}, bytes.Repeat([]byte("owned repetitive source\n"), 4096), random, []byte("after a large object"), nil}
}

// Frozen fresh-writer reference, unchanged at base75486a3c1. This comparison checks exact
// compressed bytes, independent stream trailers and complete Git pack bytes.
func legacyProjectPack(t *testing.T, objects projectGitObjects) []byte {
	t.Helper()
	var pack bytes.Buffer
	pack.WriteString("PACK")
	var header [8]byte
	binary.BigEndian.PutUint32(header[:4], 2)
	binary.BigEndian.PutUint32(header[4:], uint32(len(objects)))
	pack.Write(header[:])
	ids := make([]string, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		o := objects[id]
		kind := byte(3)
		if o.kind == "tree" {
			kind = 2
		}
		size := len(o.data)
		first := kind<<4 | byte(size&15)
		size >>= 4
		if size != 0 {
			first |= 128
		}
		pack.WriteByte(first)
		for size != 0 {
			next := byte(size & 127)
			size >>= 7
			if size != 0 {
				next |= 128
			}
			pack.WriteByte(next)
		}
		writer := zlib.NewWriter(&pack)
		if _, err := writer.Write(o.data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	checksum := sha1.Sum(pack.Bytes())
	pack.Write(checksum[:])
	return pack.Bytes()
}

func TestProjectGitCompressionPackMatchesFreshStreams(t *testing.T) {
	ctx := t.Context()
	store := &ProjectStore{Directory: t.TempDir()}
	repo, err := store.gitPath(ctx, "compression", "pack", true)
	if err != nil {
		t.Fatal(err)
	}
	objects := projectGitObjects{}
	for _, data := range projectCompressionFixtures() {
		if _, err := objects.add("blob", data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := objects.add("tree", nil); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectGitPack(ctx, repo, objects); err != nil {
		t.Fatal(err)
	} // actual index-pack --strict --threads=1
	packs, err := filepath.Glob(filepath.Join(repo, "objects", "pack", "*.pack"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("pack files=%v err=%v", packs, err)
	}
	actual, err := os.ReadFile(packs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, legacyProjectPack(t, objects)) {
		t.Fatal("pack bytes/checksum differ from independent fresh zlib streams")
	}
	for id, o := range objects {
		actual, err := runProjectGit(ctx, repo, nil, "cat-file", o.kind, id)
		if err != nil || !bytes.Equal(actual, o.data) {
			t.Fatalf("object%s changed: %v", id, err)
		}
	}
	if out, err := runProjectGit(ctx, repo, nil, "fsck", "--full"); err != nil {
		t.Fatalf("fsck %s: %v", out, err)
	}
}

func TestProjectGitCompressionLooseObjectsMatchFreshStreams(t *testing.T) {
	store := &ProjectStore{Directory: t.TempDir()}
	repo, err := store.gitPath(t.Context(), "compression", "loose", true)
	if err != nil {
		t.Fatal(err)
	}
	for i, data := range projectCompressionFixtures() {
		objects := projectGitObjects{}
		id, err := objects.add("blob", data)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeProjectGitObject(repo, id, "blob", data); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(filepath.Join(repo, "objects", id[:2], id[2:]))
		if err != nil {
			t.Fatal(err)
		}
		var expected bytes.Buffer
		writer := zlib.NewWriter(&expected)
		fmt.Fprintf(writer, "blob %d%c", len(data), byte(0))
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, expected.Bytes()) {
			t.Fatalf("object%d compressed bytes changed", i)
		}
		decoder, err := zlib.NewReader(bytes.NewReader(actual))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(decoder)
		closeErr := decoder.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("object%d stream invalid: %v/%v", i, err, closeErr)
		}
		header := []byte(fmt.Sprintf("blob %d%c", len(data), byte(0)))
		want := append(header, data...)
		if !bytes.Equal(raw, want) {
			t.Fatalf("object%d stream joined or changed", i)
		}
	}
}
