package managedlaunch

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func streamOwnedFile(t testing.TB, raw []byte) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "owned-executable")
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
func streamFixtureCorpus() [][]byte {
	out := [][]byte{nil, []byte("#!/bin/sh\n"), nativeFixture(), append(nativeFixture(), make([]byte, 1<<20)...)}
	for _, tag := range []uint64{0, 1, 30, 0x6ffffefb, 0x6ffffefc, 0x7fffffff, 0x7ffffffd} {
		raw := append(nativeFixture(), make([]byte, 32)...)
		binary.LittleEndian.PutUint16(raw[16:], 3)
		binary.LittleEndian.PutUint32(raw[64:], 2)
		binary.LittleEndian.PutUint64(raw[72:], 120)
		binary.LittleEndian.PutUint64(raw[96:], 32)
		binary.LittleEndian.PutUint64(raw[120:], tag)
		out = append(out, raw)
	}
	for _, offset := range []uint64{0, 120, 121, ^uint64(0) - 15} {
		raw := append(nativeFixture(), make([]byte, 32)...)
		binary.LittleEndian.PutUint16(raw[16:], 3)
		binary.LittleEndian.PutUint32(raw[64:], 2)
		binary.LittleEndian.PutUint64(raw[72:], offset)
		binary.LittleEndian.PutUint64(raw[96:], 32)
		out = append(out, raw)
	}
	for _, length := range []int{1, 15, 63, 64, 119} {
		out = append(out, append([]byte(nil), nativeFixture()[:length]...))
	}
	return out
}
func TestManagedStreamCaptureAndArchiveMatchLegacy(t *testing.T) {
	for i, raw := range streamFixtureCorpus() {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			want, we := legacyByteCapture("/opt/native/codex", raw)
			got, ge := Capture("/opt/native/codex", raw)
			if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
				t.Fatalf("byte qualification changed got%v/%v want%v/%v", got, ge, want, we)
			}
			file := streamOwnedFile(t, raw)
			got, ge = CaptureFile("/opt/native/codex", file)
			if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
				t.Fatalf("file qualification changed got%v/%v want%v/%v", got, ge, want, we)
			}
			for _, extra := range []bool{false, true} {
				archive := artifactArchive(t, "codex", 0555, 0, raw, extra)
				got, ge = CaptureArchive("/opt/native/codex", bytes.NewReader(archive))
				want, we = legacyArtifactArchive("/opt/native/codex", bytes.NewReader(archive))
				if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
					t.Fatalf("archive qualification changed got%v/%v want%v/%v", got, ge, want, we)
				}
				gotVerify := VerifyArchive(bytes.NewReader(archive), "codex", raw)
				wantVerify := legacyLauncherArchive(bytes.NewReader(archive), "codex", raw)
				if (gotVerify == nil) != (wantVerify == nil) {
					t.Fatal("byte archive verification changed")
				}
			}
		})
	}
}
func FuzzManagedStreamingMatchesLegacy(f *testing.F) {
	for _, raw := range streamFixtureCorpus() {
		f.Add(raw)
	}
	// A valid executable beyond the bounded prefix must not rescue that prefix.
	f.Add(append(make([]byte, 64<<10), nativeFixture()...))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64<<10 {
			// Compare this bounded prefix only, not the entire long input.
			raw = raw[:64<<10]
		}
		want, we := legacyByteCapture("/opt/native/codex", raw)
		got, ge := Capture("/opt/native/codex", raw)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatal("byte qualification differs")
		}
		file := streamOwnedFile(t, raw)
		got, ge = CaptureFile("/opt/native/codex", file)
		if (ge == nil) != (we == nil) || !reflect.DeepEqual(got, want) {
			t.Fatal("stream qualification differs")
		}
	})
}

func TestManagedBoundedFuzzPrefixRejectsExecutableTail(t *testing.T) {
	raw := append(make([]byte, 64<<10), nativeFixture()...)
	prefix := raw[:64<<10]
	if _, err := legacyByteCapture("/opt/native/codex", prefix); err == nil {
		t.Fatal("legacy capture accepted a non-executable bounded prefix")
	}
	if _, err := Capture("/opt/native/codex", prefix); err == nil {
		t.Fatal("byte capture accepted an executable located after the bound")
	}
	if _, err := CaptureFile("/opt/native/codex", streamOwnedFile(t, prefix)); err == nil {
		t.Fatal("stream capture accepted an executable located after the bound")
	}
}
func BenchmarkManagedStreamingFile(b *testing.B) {
	for _, n := range []int{1 << 20, 32 << 20, 128 << 20} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			raw := make([]byte, n)
			copy(raw, nativeFixture())
			file := streamOwnedFile(b, raw)
			b.ReportAllocs()
			b.SetBytes(int64(n))
			b.ResetTimer()
			for range b.N {
				if _, e := file.Seek(0, 0); e != nil {
					b.Fatal(e)
				}
				if _, e := CaptureFile("/opt/native/codex", file); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
func BenchmarkManagedStreamingArchive(b *testing.B) {
	for _, n := range []int{1 << 20, 32 << 20, 128 << 20} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			raw := make([]byte, n)
			copy(raw, nativeFixture())
			archive := artifactArchive(b, "codex", 0555, 0, raw, false)
			b.ReportAllocs()
			b.SetBytes(int64(n))
			b.ResetTimer()
			for range b.N {
				if _, e := CaptureArchive("/opt/native/codex", bytes.NewReader(archive)); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
