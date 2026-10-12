package managedlaunch

import (
	"archive/tar"
	"bytes"
	"testing"
)

func artifactArchive(t testing.TB, name string, mode int64, uid int, body []byte, extra bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: name, Mode: mode, Uid: uid, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if extra {
		if err := w.WriteHeader(&tar.Header{Name: "extra", Mode: 0555, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestManagedArchiveAdmission(t *testing.T) {
	raw := nativeFixture()
	good := artifactArchive(t, "codex", 0555, 0, raw, false)
	expected, err := Capture("/opt/native/codex", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := CaptureArchive("/opt/native/codex", bytes.NewReader(good)); err != nil || got.SHA256 != expected.SHA256 {
		t.Fatalf("positive control: %v %v", got, err)
	}
	if err := VerifyArchive(bytes.NewReader(good), "codex", raw); err != nil {
		t.Fatalf("comparison positive control: %v", err)
	}
	for name, archive := range map[string][]byte{
		"traversal": artifactArchive(t, "../codex", 0555, 0, raw, false),
		"extra":     artifactArchive(t, "codex", 0555, 0, raw, true),
		"truncated": good[:520],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CaptureArchive("/opt/native/codex", bytes.NewReader(archive)); err == nil {
				t.Fatal("invalid artifact archive admitted")
			}
			if err := VerifyArchive(bytes.NewReader(archive), "codex", raw); err == nil {
				t.Fatal("invalid comparison archive admitted")
			}
		})
	}
	for name, archive := range map[string][]byte{
		"writable": artifactArchive(t, "codex", 0777, 0, raw, false),
		"owner":    artifactArchive(t, "codex", 0555, 1002, raw, false),
		"nonexec":  artifactArchive(t, "codex", 0444, 0, raw, false),
		"script":   artifactArchive(t, "codex", 0555, 0, []byte("#!/bin/sh\n"), false),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CaptureArchive("/opt/native/codex", bytes.NewReader(archive)); err == nil {
				t.Fatal("unsafe image artifact admitted")
			}
		})
	}
	if err := VerifyArchive(bytes.NewReader(good), "codex", []byte("substituted")); err == nil {
		t.Fatal("substituted bytes accepted")
	}
}

func FuzzManagedArtifactArchive(f *testing.F) {
	f.Add(artifactArchive(f, "codex", 0555, 0, nativeFixture(), false))
	f.Add(artifactArchive(f, "../codex", 0555, 0, nativeFixture(), false))
	f.Add(artifactArchive(f, "codex", 0555, 0, nativeFixture(), true))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		artifact, err := CaptureArchive("/opt/native/codex", bytes.NewReader(raw))
		if err == nil && (artifact == nil || artifact.Path != "/opt/native/codex" || artifact.Format != "static_elf") {
			t.Fatal("invalid artifact evidence")
		}
	})
}

func FuzzManagedLauncherArchive(f *testing.F) {
	native := nativeFixture()
	f.Add(artifactArchive(f, "launcher", 0555, 0, native, false), native)
	f.Add(artifactArchive(f, "launcher", 0555, 0, native, true), native)
	f.Fuzz(func(t *testing.T, raw, expected []byte) {
		if len(raw) > 1<<20 || len(expected) > 1<<20 {
			return
		}
		if VerifyArchive(bytes.NewReader(raw), "launcher", expected) == nil {
			h := tar.NewReader(bytes.NewReader(raw))
			entry, err := h.Next()
			if err != nil || entry.Name != "launcher" || entry.Typeflag != tar.TypeReg || entry.Size != int64(len(expected)) {
				t.Fatal("invalid archive evidence accepted")
			}
		}
	})
}
