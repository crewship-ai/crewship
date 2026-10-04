package managedlaunch

import (
	"encoding/binary"
	"strings"
	"testing"
)

func nativeFixture() []byte {
	raw := make([]byte, 120)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 62)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint32(raw[64:], 1)
	return raw
}

func TestManagedArtifactRejectsUnsupportedInputs(t *testing.T) {
	good, err := Capture("/opt/native/claude", nativeFixture())
	if err != nil || good.Format != "static_elf" || len(good.SHA256) != 64 {
		t.Fatalf("static capture=%+v %v", good, err)
	}
	for _, p := range []string{"claude", "/home/agent/claude", "/tmp/claude", "/opt/../home/claude", LauncherPath} {
		if _, err := Capture(p, nativeFixture()); err == nil {
			t.Fatalf("mutable path accepted: %s", p)
		}
	}
	for _, kind := range []uint32{2, 3} {
		raw := nativeFixture()
		binary.LittleEndian.PutUint32(raw[64:], kind)
		if _, err := Capture("/opt/native/claude", raw); err == nil {
			t.Fatalf("loader-bearing ELF accepted: %d", kind)
		}
	}
	if _, err := Capture("/opt/native/claude", []byte("#!/usr/bin/env node\n")); err == nil {
		t.Fatal("env-shebang accepted")
	}
}

func TestManagedEnvironmentDiscardsImageAndLoaderInjection(t *testing.T) {
	input := []string{"HOME=/home/agent", "PATH=/home/agent/.local/bin", "LD_PRELOAD=/home/agent/evil.so", "NODE_OPTIONS=--require /home/evil", "ANTHROPIC_API_KEY=EXAMPLE-SYNTHETIC", "GLIBC_TUNABLES=evil", "ENV=/home/evil", "TMUX=old-socket"}
	values, keys, err := Environment(input)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(values, "\n"); strings.Contains(joined, "evil") || strings.Contains(joined, "old-socket") || !strings.Contains(joined, "PATH="+SafePath) {
		t.Fatal(joined)
	}
	got, err := SelectedEnvironment(Descriptor{EnvKeys: keys}, append(values, "IMAGE_SECRET=EXAMPLE", "LD_AUDIT=evil"))
	if err != nil || strings.Contains(strings.Join(got, "\n"), "IMAGE_SECRET") {
		t.Fatalf("image inheritance=%v %v", got, err)
	}
	if _, err := SelectedEnvironment(Descriptor{EnvKeys: []string{"LD_PRELOAD"}}, input); err == nil {
		t.Fatal("forged loader selection accepted")
	}
	if _, err := SelectedEnvironment(Descriptor{EnvKeys: []string{"HOME"}}, nil); err == nil {
		t.Fatal("missing authoritative environment accepted")
	}
}

func TestManagedArtifactStaticPIERejectsLoaderDependencies(t *testing.T) {
	for _, kind := range []string{"static-pie", "needed", "interpreter", "truncated", "unterminated", "audit"} {
		t.Run(kind, func(t *testing.T) {
			raw := append(nativeFixture(), make([]byte, 32)...)
			binary.LittleEndian.PutUint16(raw[16:], 3)
			binary.LittleEndian.PutUint32(raw[64:], 2)
			binary.LittleEndian.PutUint64(raw[72:], 120)
			binary.LittleEndian.PutUint64(raw[96:], 32)
			binary.LittleEndian.PutUint64(raw[120:], 30) // DT_FLAGS, followed by DT_NULL
			switch kind {
			case "needed":
				binary.LittleEndian.PutUint64(raw[120:], 1)
			case "interpreter":
				binary.LittleEndian.PutUint32(raw[64:], 3)
			case "truncated":
				raw = raw[:151]
			case "unterminated":
				binary.LittleEndian.PutUint64(raw[136:], 30)
			case "audit":
				binary.LittleEndian.PutUint64(raw[120:], 0x6ffffefc)
			}
			artifact, err := Capture("/opt/native/codex", raw)
			if kind == "static-pie" {
				if err != nil || artifact == nil {
					t.Fatalf("static PIE rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("loader-bearing or malformed PIE admitted")
			}
		})
	}
}

func TestManagedEnvironmentKeepsOnlyAuthoritativeLowercaseProxies(t *testing.T) {
	values, keys, err := Environment([]string{"http_proxy=http://fixture:9119", "https_proxy=http://fixture:9119", "no_proxy=localhost", "arbitrary_lowercase=discard", "PATH=/opt/mise/data/installs/python/3/bin:/home/agent/bin"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := SelectedEnvironment(Descriptor{EnvKeys: keys}, append(values, "image_only=discard", "NODE_OPTIONS=discard"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"http_proxy=http://fixture:9119", "https_proxy=http://fixture:9119", "no_proxy=localhost", "PATH=" + SafePath} {
		if !strings.Contains(joined, want) {
			t.Fatal(joined)
		}
	}
	if strings.Contains(joined, "discard") || strings.Contains(joined, "/opt/mise") || strings.Contains(joined, "/home/agent/bin") {
		t.Fatal(joined)
	}
}

func TestManagedDescriptorRejectsUnsafeRunIdentity(t *testing.T) {
	for _, id := range []string{"", "../escape", "x/y", "x'", strings.Repeat("a", 81)} {
		if ValidRunID(id) {
			t.Fatalf("unsafe identity accepted: %q", id)
		}
	}
}
