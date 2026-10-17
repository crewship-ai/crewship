package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func canonical(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../.golangci.yml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func configMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := yaml.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func section(value map[string]any, key string) map[string]any { return value[key].(map[string]any) }

func TestPartitionsPreserveCanonicalConfiguration(t *testing.T) {
	original := configMap(t, canonical(t))
	section(original, "run")["future_setting"] = map[string]any{"nested": []any{"unchanged", true, 42}}
	section(original, "linters")["future_setting"] = map[string]any{"keep": "all unknown settings"}
	section(original, "formatters")["settings"] = map[string]any{"gofmt": map[string]any{"rewrite-rules": []any{"a[b:len(a)] -> a[b:]"}}}
	raw, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := partitions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Name != "checks" || groups[1].Name != "unused" {
		t.Fatalf("partition order: %+v", groups)
	}
	want := [][]string{{"gofmt", "govet", "ineffassign", "staticcheck"}, {"unused"}}
	seen := map[string]bool{}
	for i, group := range groups {
		got := configMap(t, group.Config)
		var enabled []string
		for _, key := range []string{"linters", "formatters"} {
			for _, item := range section(got, key)["enable"].([]any) {
				name := item.(string)
				if seen[name] {
					t.Fatalf("duplicate check %s", name)
				}
				seen[name] = true
				enabled = append(enabled, name)
			}
			section(got, key)["enable"] = section(original, key)["enable"]
		}
		// Only the two enable lists may change, including every future setting.
		if !reflect.DeepEqual(got, original) {
			t.Fatalf("%s changed nested configuration", group.Name)
		}
		for _, name := range want[i] {
			if !contains(enabled, name) {
				t.Fatalf("%s missing %s: %v", group.Name, name, enabled)
			}
		}
		if len(enabled) != len(want[i]) {
			t.Fatalf("unexpected checks: %v", enabled)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("incomplete union: %v", seen)
	}
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func TestPartitionsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"new linter", func(v map[string]any) {
			section(v, "linters")["enable"] = []string{"govet", "ineffassign", "staticcheck", "unused", "errcheck"}
		}},
		{"duplicate linter", func(v map[string]any) {
			section(v, "linters")["enable"] = []string{"govet", "ineffassign", "staticcheck", "unused", "unused"}
		}},
		{"missing linter", func(v map[string]any) { section(v, "linters")["enable"] = []string{"govet", "ineffassign", "unused"} }},
		{"new formatter", func(v map[string]any) { section(v, "formatters")["enable"] = []string{"gofmt", "goimports"} }},
		{"duplicate formatter", func(v map[string]any) { section(v, "formatters")["enable"] = []string{"gofmt", "gofmt"} }},
		{"defaults", func(v map[string]any) { section(v, "linters")["default"] = "standard" }},
		{"disabled", func(v map[string]any) { section(v, "linters")["disable"] = []string{"unused"} }},
		{"tests", func(v map[string]any) { section(v, "run")["tests"] = false }},
		{"timeout", func(v map[string]any) { section(v, "run")["timeout"] = "10m" }},
		{"issue limit", func(v map[string]any) { section(v, "issues")["max-same-issues"] = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := configMap(t, canonical(t))
			test.mutate(value)
			raw, err := yaml.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = partitions(raw); err == nil {
				t.Fatal("changed coverage accepted")
			}
		})
	}
	if _, err := partitions([]byte("version: '2'\nversion: '2'\n")); err == nil {
		t.Fatal("duplicate YAML keys accepted")
	}
}

func TestWorkflowKeepsBudgetsAndUsesPartitions(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	jobs := section(configMap(t, raw), "jobs")
	job := section(jobs, "go-lint")
	if job["timeout-minutes"] != 15 {
		t.Fatal("job budget changed")
	}
	for _, value := range job["steps"].([]any) {
		step := value.(map[string]any)
		if step["name"] == "Run golangci-lint" {
			command := step["run"].(string)
			if step["timeout-minutes"] != 8 || !strings.HasPrefix(command, "lint_package=scripts/golangci-shards\n") || !strings.Contains(command, `go run "./$lint_package"`) {
				t.Fatalf("unpartitioned or changed step: %v", step)
			}
			return
		}
	}
	t.Fatal("missing lint step")
}

func TestWorkflowProtectsLinterHelper(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the actual trusted guard parser. A ./scripts literal is not
	// recognized by its boundary rule and would leave this helper unprotected.
	command := exec.Command("python3", "-B", "-c", `import importlib.util,json,pathlib,sys
root=pathlib.Path(sys.argv[1])
spec=importlib.util.spec_from_file_location("guard",root/"scripts/ci/inventory-guard.py")
guard=importlib.util.module_from_spec(spec);spec.loader.exec_module(guard)
print(json.dumps(sorted(guard.referenced_scripts(root))))`, root)
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("trusted guard parser: %v %s", err, raw)
	}
	var refs []string
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatal(err)
	}
	if !contains(refs, "scripts/golangci-shards") {
		t.Fatalf("lint helper directory is outside the trusted control inventory: %v", refs)
	}
}

// Process-group qualification executes on the Ubuntu CI platform. Configuration
// contracts above remain portable and run on every Go test platform.
func runnerFixture(t *testing.T, script string) (string, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Ubuntu CI process-group execution")
	}
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
