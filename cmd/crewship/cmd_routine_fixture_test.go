package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutineFixtureCLIReportsEvidenceAndFailsBadOutput(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "recipe.json")
	definition := `{"name":"fixture-cli","inputs":[{"name":"count","type":"integer","widget":"number","default":0}],"steps":[{"id":"a","type":"transform","transform":{"input":"{{ inputs.count }}","expression":"."},"validation":{"must_contain":["0"]}}]}`
	if err := os.WriteFile(file, []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "7"} {
		cmd := newRoutineFixtureTestCmd()
		if err := cmd.ParseFlags([]string{"--step", "a", "--input", `{"count":` + value + `}`}); err != nil {
			t.Fatal(err)
		}
		out, err := captureStdoutCovCli10(t, func() error { return cmd.RunE(cmd, []string{file}) })
		if (err == nil) != (value == "0") {
			t.Fatalf("value=%s err=%v out=%s", value, err, out)
		}
		if !strings.Contains(out, `"execution_mode": "fixtures"`) || !strings.Contains(out, `"fixture_hash"`) {
			t.Fatal("missing evidence", out)
		}
	}
}

func TestRoutineFixtureCLIRuntimeSamples(t *testing.T) {
	file := filepath.Join(t.TempDir(), "recipe.json")
	if err := os.WriteFile(file, []byte(`{"name":"fixture-context","steps":[{"id":"sample","type":"transform","transform":{"input":"{{ env.run_id }}|{{ run.metadata.count }}|{{ secrets.example }}","expression":"."}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newRoutineFixtureTestCmd()
	if err := cmd.ParseFlags([]string{"--step", "sample", "--env", `{"run_id":"sample-run"}`, "--metadata", `{"count":0}`, "--secrets", `{"example":"fake"}`}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdoutCovCli10(t, func() error { return cmd.RunE(cmd, []string{file}) })
	if err != nil || !strings.Contains(out, "sample-run|0|fake") {
		t.Fatalf("%s: %v", out, err)
	}
}
