package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Process-group qualification executes on the Ubuntu CI platform. Configuration
// contracts above remain portable and run on every Go test platform.
func runnerFixture(t *testing.T, script string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), canonical(t), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "linter")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHARD_TEST_DIR", dir)
	return dir, binary
}

func TestRunnerExecutesExactConfigsAndCleans(t *testing.T) {
	dir, binary := runnerFixture(t, `count=$(cat "$SHARD_TEST_DIR/count" 2>/dev/null || echo 0)
count=$((count+1))
echo "$count" > "$SHARD_TEST_DIR/count"
printf '%s\n' "$@" > "$SHARD_TEST_DIR/args$count"
pwd > "$SHARD_TEST_DIR/cwd$count"
while [ "$#" -gt 0 ]; do
  if [ "$1" = --config ]; then cp "$2" "$SHARD_TEST_DIR/config$count"; fi
  shift
done
`)
	var output bytes.Buffer
	if err := run(context.Background(), dir, binary, &output, &output); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		args, err := os.ReadFile(filepath.Join(dir, "args"+strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(args), "--timeout=5m\n") || !strings.HasSuffix(string(args), "./...\n") {
			t.Fatalf("wrong invocation %s", args)
		}
		cwd, _ := os.ReadFile(filepath.Join(dir, "cwd"+strconv.Itoa(i)))
		if strings.TrimSpace(string(cwd)) != dir {
			t.Fatalf("wrong cwd %s", cwd)
		}
		cfg, _ := os.ReadFile(filepath.Join(dir, "config"+strconv.Itoa(i)))
		enabled := section(configMap(t, cfg), "linters")["enable"].([]any)
		if (i == 2) != (len(enabled) == 1 && enabled[0] == "unused") {
			t.Fatal("wrong order")
		}
	}
	assertClean(t, dir)
}

func assertClean(t *testing.T, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, ".golangci-shard-*.yml"))
	if err != nil || len(files) != 0 {
		t.Fatalf("leaked configs %v: %v", files, err)
	}
}

func TestRunnerFailureAndCancellationCleanOwnFiles(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		dir, binary := runnerFixture(t, "exit 7\n")
		if err := run(context.Background(), dir, binary, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatal("failure accepted")
		}
		assertClean(t, dir)
	})
	t.Run("cancel children", func(t *testing.T) {
		dir, binary := runnerFixture(t, `sh -c 'echo $$ > "$SHARD_TEST_DIR/child"; while :; do sleep 1; done' &
wait
`)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- run(ctx, dir, binary, &bytes.Buffer{}, &bytes.Buffer{}) }()
		deadline := time.Now().Add(5 * time.Second)
		var child []byte
		for time.Now().Before(deadline) {
			child, _ = os.ReadFile(filepath.Join(dir, "child"))
			if len(child) > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if len(child) == 0 {
			cancel()
			t.Fatal("child did not start")
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancel accepted")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancel hung")
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(child)))
		if err != nil {
			t.Fatal(err)
		}
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
			if os.IsNotExist(err) || strings.Contains(string(raw), ") Z ") {
				assertClean(t, dir)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("own descendant survived cancellation")
	})
}

func TestRunnerPreservesExistingSiblingSymlink(t *testing.T) {
	dir, binary := runnerFixture(t, "exit 0\n")
	target := filepath.Join(dir, "unrelated")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, ".golangci-shard-existing.yml")
	if err := os.Symlink(target, foreign); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), dir, binary, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "untouched" {
		t.Fatalf("overwrote sibling target: %s %v", raw, err)
	}
	files, err := filepath.Glob(filepath.Join(dir, ".golangci-shard-*.yml"))
	if err != nil || len(files) != 1 || files[0] != foreign {
		t.Fatalf("touched unowned sibling or leaked config: %v %v", files, err)
	}
}

func TestRunnerCancellationDuringStartup(t *testing.T) {
	dir, binary := runnerFixture(t, "sleep 30\n")
	for _, delay := range []time.Duration{0, time.Microsecond, time.Millisecond} {
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(delay, cancel)
		err := run(ctx, dir, binary, &bytes.Buffer{}, &bytes.Buffer{})
		timer.Stop()
		cancel()
		if err == nil {
			t.Fatal("cancelled startup accepted")
		}
		assertClean(t, dir)
	}
}
