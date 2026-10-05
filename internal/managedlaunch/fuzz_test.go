package managedlaunch

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func fuzzDescriptor() Descriptor {
	return Descriptor{Artifact: Artifact{Path: "/opt/native/codex", SHA256: strings.Repeat("0", 64), Format: "static_elf"}, ImageID: "sha256:" + strings.Repeat("1", 64), RevisionID: "revision", LockSHA256: strings.Repeat("2", 64), Binary: "codex", Version: "1.0.0", RunID: "conformance"}
}

func FuzzManagedDescriptor(f *testing.F) {
	raw, _ := json.Marshal(fuzzDescriptor())
	f.Add(base64.RawURLEncoding.EncodeToString(raw))
	f.Add("!")
	f.Add(base64.RawURLEncoding.EncodeToString([]byte(`{"path":"/opt/a","path":"/opt/b"}`)))
	f.Fuzz(func(t *testing.T, encoded string) {
		d, err := DecodeDescriptor(encoded)
		if err != nil {
			return
		}
		if d.Validate() != nil || !ValidRunID(d.RunID) {
			t.Fatal("invalid descriptor admitted")
		}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		again, err := DecodeDescriptor(base64.RawURLEncoding.EncodeToString(raw))
		if err != nil || again.SHA256 != d.SHA256 || again.RunID != d.RunID || again.Path != d.Path {
			t.Fatal("unstable descriptor round trip")
		}
	})
}

func FuzzManagedELFCapture(f *testing.F) {
	f.Add("/opt/native/codex", nativeFixture())
	f.Add("/home/agent/codex", []byte("#!/bin/sh\n"))
	f.Fuzz(func(t *testing.T, name string, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		artifact, err := Capture(name, raw)
		if err != nil {
			return
		}
		sum := sha256.Sum256(raw)
		if artifact == nil || !ImagePath(artifact.Path) || artifact.Format != "static_elf" || artifact.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("invalid artifact evidence")
		}
	})
}

func FuzzManagedEnvironment(f *testing.F) {
	f.Add("HOME=/home/agent\nLD_PRELOAD=evil\nhttp_proxy=http://fixture")
	f.Add("ENV=evil\nBASH_ENV=evil\nPATH=/home/agent/bin")
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 64<<10 {
			return
		}
		values, keys, err := Environment(strings.Split(text, "\n"))
		if err != nil {
			return
		}
		selected, err := SelectedEnvironment(Descriptor{EnvKeys: keys}, append(values, "UNSELECTED_IMAGE_VALUE=untrusted"))
		if err != nil {
			return
		} // selection intentionally caps authoritative keys at256.
		if selected[0] != "PATH="+SafePath {
			t.Fatal("untrusted PATH admitted")
		}
		wanted := map[string]bool{}
		for _, key := range keys {
			wanted[key] = true
		}
		for _, item := range selected[1:] {
			key, _, ok := strings.Cut(item, "=")
			if !ok || !AllowedEnvKey(key) || !wanted[key] {
				t.Fatal("unselected or unsafe environment admitted")
			}
		}
	})
}

func FuzzManagedProcessIdentity(f *testing.F) {
	f.Add([]byte("123 (native) S 1 123 123 0 0 0 0 0 0 0 0 0 0 0 0 0 0 17"), 123)
	f.Add([]byte("broken"), 1)
	f.Fuzz(func(t *testing.T, raw []byte, pid int) {
		if len(raw) > 64<<10 {
			return
		}
		stamp, err := processStartIdentity(raw, pid)
		if err == nil && (stamp == 0 || pid <= 0) {
			t.Fatal("invalid identity accepted")
		}
	})
}
