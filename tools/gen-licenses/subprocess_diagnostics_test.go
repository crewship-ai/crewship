package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("CREWSHIP_LICENSE_TEST_GO"); mode != "" {
		fmt.Println("module-sentinel")
		diagnostic := "stderr-cause-sentinel"
		if mode != "failure" {
			diagnostic = "stderr-head" + strings.Repeat("x", 20_000) + "stderr-tail"
		}
		fmt.Fprint(os.Stderr, diagnostic)
		if mode != "success" {
			os.Exit(7)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeGo(t *testing.T, mode string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	root, name := t.TempDir(), "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(root, name), binary, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_LICENSE_TEST_GO", mode)
	t.Setenv("PATH", root)
}

func TestLinkedModulesReportsFailedGoStderr(t *testing.T) {
	fakeGo(t, "failure")
	_, _, err := linkedModules()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("lost original subprocess failure: %v", err)
	}
	for _, sentinel := range []string{"GOOS=linux GOARCH=amd64", "module-sentinel", "stderr-cause-sentinel"} {
		if !strings.Contains(err.Error(), sentinel) {
			t.Errorf("failure omitted %q: %v", sentinel, err)
		}
	}
}

func TestLinkedModulesBoundsFailureDiagnosticsAndKeepsSuccessStdoutSeparate(t *testing.T) {
	for _, mode := range []string{"bounded", "success"} {
		t.Run(mode, func(t *testing.T) {
			fakeGo(t, mode)
			_, modules, err := linkedModules()
			if mode == "success" {
				if err != nil || len(modules) != 1 || modules[0] != "module-sentinel" {
					t.Fatalf("successful stdout was contaminated: %v %v", modules, err)
				}
				return
			}
			if err == nil || len(err.Error()) > 8500 || !strings.Contains(err.Error(), "stderr-head") || !strings.Contains(err.Error(), "stderr-tail") || !strings.Contains(err.Error(), "stderr truncated") {
				t.Fatalf("failure diagnostic lost its bounded head/tail: %v", err)
			}
		})
	}
}
