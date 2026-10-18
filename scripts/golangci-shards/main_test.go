package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
