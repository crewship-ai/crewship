//go:build !windows

package runtimestage

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Structural ELF fixtures are never executed and do not qualify CLI identity,
// packaging, container availability or authentication.
func agentCLIStructuralELF(machine uint16) []byte {
	raw := make([]byte, 120)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], machine)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint32(raw[64:], 1)
	return raw
}

func TestAgentCLIStagingRetainsImmutableGenerations(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "crewship-agent-cli")
	first := agentCLIStructuralELF(62)
	if err := os.WriteFile(source, first, 0755); err != nil {
		t.Fatal(err)
	}
	a, err := StageAgentCLI(root, source)
	if err != nil {
		t.Fatal(err)
	}
	if a.Arch != "amd64" || len(a.SHA256) != 64 || filepath.Base(a.Path) != "crewship-agent-cli-"+a.SHA256 {
		t.Fatal("missing qualified target/digest generation")
	}
	info, err := os.Stat(a.Path)
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatal("staged agent CLI must be immutable executable")
	}
	again, err := StageAgentCLI(root, source)
	if err != nil || again.Path != a.Path {
		t.Fatal("unchanged input must reuse its verified generation")
	}
	againInfo, err := os.Stat(again.Path)
	if err != nil || !os.SameFile(info, againInfo) {
		t.Fatal("staging replaced an existing generation inode")
	}
	second := append(agentCLIStructuralELF(183), []byte("new-generation")...)
	if err := os.WriteFile(source, second, 0755); err != nil {
		t.Fatal(err)
	}
	b, err := StageAgentCLI(root, source)
	if err != nil || b.Path == a.Path || b.Arch != "arm64" {
		t.Fatal("updated artifact must create a distinct generation")
	}
	old, err := os.ReadFile(a.Path)
	if err != nil || !bytes.Equal(old, first) {
		t.Fatal("upgrade changed old generation used by a live runtime")
	}
}

func TestAgentCLIStagingRejectsUntrustedInputs(t *testing.T) {
	for _, mutation := range []string{"script", "macho", "dynamic", "source-symlink", "staging-symlink", "existing-symlink", "existing-hardlink", "existing-writable", "existing-corrupt"} {
		t.Run(mutation, func(t *testing.T) {
			root, install := t.TempDir(), t.TempDir()
			source := filepath.Join(install, "crewship-agent-cli")
			raw := agentCLIStructuralELF(62)
			if mutation == "script" {
				raw = []byte("#!/bin/sh\nexit 0\n")
			}
			if mutation == "macho" {
				copy(raw, []byte{0xcf, 0xfa, 0xed, 0xfe})
			}
			if mutation == "dynamic" {
				binary.LittleEndian.PutUint32(raw[64:], 3)
			}
			if err := os.WriteFile(source, raw, 0755); err != nil {
				t.Fatal(err)
			}
			if mutation == "source-symlink" {
				target := source + ".real"
				if err := os.Rename(source, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, source); err != nil {
					t.Fatal(err)
				}
			}
			if mutation == "staging-symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(root, DirName)); err != nil {
					t.Fatal(err)
				}
			}
			if len(mutation) > 9 && mutation[:9] == "existing-" {
				a, err := StageAgentCLI(root, source)
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "existing-symlink":
					if err := os.Remove(a.Path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(source, a.Path); err != nil {
						t.Fatal(err)
					}
				case "existing-hardlink":
					if err := os.Link(a.Path, a.Path+".alias"); err != nil {
						t.Fatal(err)
					}
				case "existing-writable":
					if err := os.Chmod(a.Path, 0755); err != nil {
						t.Fatal(err)
					}
				case "existing-corrupt":
					if err := os.Remove(a.Path); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(a.Path, []byte("changed"), 0555); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := StageAgentCLI(root, source); err == nil {
				t.Fatal("unsafe artifact or generation accepted")
			}
		})
	}
}
