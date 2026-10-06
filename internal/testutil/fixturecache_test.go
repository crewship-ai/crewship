package testutil

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each child has its own sync.Once and process lifetime; copying the compiled
// test binary to two paths exercises separate binaries against one temp root.
func TestFixtureCacheProcessHelper(t *testing.T) {
	mode := os.Getenv("CREWSHIP_FIXTURE_HELPER")
	if mode == "" {
		return
	}
	if mode == "template" {
		fmt.Println(MigratedTemplatePath(t))
		return
	}
	if mode == "collect" {
		_, cleanup, err := NewMigratedDB()
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		return
	}
	var path string
	if mode == "fixture" {
		path = MigratedDB(t).Path()
	} else {
		db, _, err := NewMigratedDB()
		if err != nil {
			t.Fatal(err)
		}
		path = db.Path()
	}
	fmt.Println(path)
	// Parent kills this child, deliberately bypassing fixture teardown.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func fixtureChild(binary, tmp, mode string) *exec.Cmd {
	cmd := exec.Command(binary, "-test.run=^TestFixtureCacheProcessHelper$")
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp, "CREWSHIP_FIXTURE_HELPER="+mode)
	return cmd
}

func TestSharedTemplateAcrossBinaries(t *testing.T) {
	tmp := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 2)
	outputs := make([]strings.Builder, 2)
	for i := range commands {
		binary := filepath.Join(t.TempDir(), "fixture.test")
		if err := copyFileForTest(executable, binary); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(binary, 0o700); err != nil {
			t.Fatal(err)
		}
		commands[i] = fixtureChild(binary, tmp, "template")
		commands[i].Stdout = &outputs[i]
		commands[i].Stderr = &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	paths := make([]string, 2)
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child: %v\n%s", err, outputs[i].String())
		}
		paths[i] = strings.Split(outputs[i].String(), "\n")[0]
	}
	if paths[0] != paths[1] {
		t.Fatalf("binaries built separate templates: %q vs %q", paths[0], paths[1])
	}
	info, err := os.Stat(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	// A subsequent binary must reuse the file, including its modification time.
	output, err := fixtureChild(executable, tmp, "template").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(output), paths[0]+"\n") {
		t.Fatalf("third binary: %v\n%s", err, output)
	}
	after, err := os.Stat(paths[0])
	if err != nil || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("template rebuilt on reuse: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(filepath.Dir(paths[0])))
	if err != nil {
		t.Fatal(err)
	}
	var templates int
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "template-") {
			templates++
		}
	}
	if templates != 1 {
		t.Fatalf("got %d template directories for three binaries, want 1", templates)
	}
}

func TestSharedTemplateInvalidationAndInterruptedBuild(t *testing.T) {
	root := t.TempDir()
	builds := 0
	build := func(path string) error {
		builds++
		return os.WriteFile(path, []byte("complete template"), 0o600)
	}
	first, err := sharedTemplate(root, "migration-hash-a", build)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sharedTemplate(root, "migration-hash-a", build); err != nil || builds != 1 {
		t.Fatalf("unchanged migrations rebuilt: builds=%d err=%v", builds, err)
	}
	second, err := sharedTemplate(root, "migration-hash-b", build)
	if err != nil || first == second || builds != 2 {
		t.Fatalf("changed migrations did not rebuild: builds=%d err=%v", builds, err)
	}
	dir := filepath.Join(root, "template-v1-interrupted")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "building.db"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := sharedTemplate(root, "interrupted", build)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete template" {
		t.Fatalf("published incomplete build: %q %v", data, err)
	}
}

func TestReclaimFixturesPreservesLiveOwner(t *testing.T) {
	template, err := templatePath()
	if err != nil {
		t.Fatal(err)
	}
	key, err := fixtureTemplateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"fixture", "new-fixture"} {
		t.Run(mode, func(t *testing.T) {
			tmp := t.TempDir()
			// This boundary tests owner liveness, not cold migration throughput.
			// Reuse a complete template so -race does not spend the readiness
			// deadline interpreting the entire migration chain in each child.
			root := filepath.Join(tmp, filepath.Base(filepath.Dir(filepath.Dir(template))))
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := sharedTemplate(root, key, func(path string) error {
				return copyFileForTest(template, path)
			}); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := fixtureChild(executable, tmp, mode)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			line := make(chan string, 1)
			go func() { scanner := bufio.NewScanner(stdout); scanner.Scan(); line <- scanner.Text() }()
			var path string
			select {
			case path = <-line:
			case <-time.After(30 * time.Second):
				t.Fatal("child failed to create fixture")
			}
			if path == "" {
				t.Fatal("child returned no fixture path")
			}
			dir := filepath.Dir(path)
			if filepath.Dir(dir) != root {
				t.Fatalf("child fixture escaped cache: %s", dir)
			}
			unknown := filepath.Join(root, "testdb-unknown-owner")
			if err := os.Mkdir(unknown, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := reclaimDeadFixtures(root); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("deleted live owner's fixture: %v", err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait() // Reap before the liveness probe.
			// A fresh fixture-owning binary must collect automatically through
			// the public entrypoint, without an explicit cleanup call by us.
			if output, err := fixtureChild(executable, tmp, "collect").CombinedOutput(); err != nil {
				t.Fatalf("collecting binary: %v\n%s", err, output)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("dead owner's fixture remains: %v", err)
			}
			if _, err := os.Stat(unknown); err != nil {
				t.Fatalf("deleted fixture with unknown ownership: %v", err)
			}
		})
	}
}

func TestMigratedFixtureCleanup(t *testing.T) {
	var path string
	t.Run("registered cleanup", func(t *testing.T) { path = MigratedDB(t).Path() })
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("registered cleanup left directory: %v", err)
	}
	db, cleanup, err := NewMigratedDB()
	if err != nil {
		t.Fatal(err)
	}
	path = db.Path()
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("explicit cleanup left directory: %v", err)
	}
}
