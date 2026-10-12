//go:build linux

package managedlaunch

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Replace or unlink the pathname after reading and qualifying its open inode.
// The second process must still be the verified ELF, with literal argv/env.
func TestManagedExecVerifiedDescriptorSurvivesPathSubstitution(t *testing.T) {
	if os.Getenv("CREWSHIP_FD_TEST_PHASE") == "verify" {
		p := os.Getenv("CREWSHIP_FD_TEST_PATH")
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		raw, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := Capture("/opt/native/codex", raw)
		if err != nil || artifact == nil {
			t.Fatalf("qualification: %v", err)
		}
		if os.Getenv("CREWSHIP_FD_TEST_MODE") == "replace" {
			replacement := p + ".replacement"
			if err := os.WriteFile(replacement, []byte("#!/bin/sh\nprintf PATH_SUBSTITUTION\n"), 0555); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, p); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		err = execVerifiedFile(f, []string{p, "literal $(false) ' argument"}, []string{"CREWSHIP_FD_TEST_PHASE=executed"})
		t.Fatalf("verified fd exec returned: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Build a static fixture separately: a -race test binary can have a
	// dynamic loader and is intentionally outside the admitted ELF boundary.
	fixtureDir := t.TempDir()
	source := filepath.Join(fixtureDir, "native.go")
	if err := os.WriteFile(source, []byte(`package main
import ("fmt"; "os")
func main() {
 if os.Getenv("CREWSHIP_FD_TEST_PHASE") != "executed" || len(os.Args) != 2 || os.Args[1] != "literal $(false) ' argument" { os.Exit(92) }
 fmt.Print("VERIFIED_INODE")
}`), 0600); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(fixtureDir, "native")
	build := exec.Command("go", "build", "-o", native, source)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("static fixture build: %v %s", err, out)
	}
	raw, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"replace", "unlink"} {
		t.Run(mode, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "verified")
			if err := os.WriteFile(p, raw, 0555); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(self, "-test.run=^TestManagedExecVerifiedDescriptorSurvivesPathSubstitution$")
			cmd.Env = append(os.Environ(), "CREWSHIP_FD_TEST_PHASE=verify", "CREWSHIP_FD_TEST_PATH="+p, "CREWSHIP_FD_TEST_MODE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "VERIFIED_INODE" {
				t.Fatalf("substitution executed or verified inode lost: %v %s", err, out)
			}
		})
	}
}
