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
