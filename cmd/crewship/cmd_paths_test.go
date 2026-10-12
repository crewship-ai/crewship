//go:build !clionly

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func pathsTestEnvironment(t *testing.T) {
	t.Helper()
	saveCLIState(t)
	flagFormat = "json"
	for _, name := range []string{"CREWSHIP_HOME", "CREWSHIP_DATA_DIR", "DATABASE_URL", "CREWSHIP_STORAGE_BASE_PATH", "CREWSHIP_LOG_PATH", "CREWSHIP_STORAGE_MEMORY_ROOT", "CREWSHIP_BOLT_PATH", "CREWSHIP_SOCKET_PATH", "CREWSHIP_PAGE_PROJECTS_PATH"} {
		t.Setenv(name, "")
	}
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
}

func inspectPaths(t *testing.T, flags map[string]string) installationPathsReport {
	t.Helper()
	cmd := newPathsCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	for name, value := range flags {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var report installationPathsReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON %q: %v", output.String(), err)
	}
	return report
}

func pathRows(report installationPathsReport) map[string]installationPathRow {
	rows := make(map[string]installationPathRow)
	for _, row := range report.Paths {
		rows[row.Resource] = row
	}
	return rows
}

func TestPathsNonexistentHomeDoesNotWriteOrLoadClientProfile(t *testing.T) {
	pathsTestEnvironment(t)
	parent := t.TempDir()
	home := filepath.Join(parent, "never-started")
	t.Setenv("CREWSHIP_HOME", home)
	clientConfig := filepath.Join(parent, "missing-client-config.yaml")
	t.Setenv("CREWSHIP_CONFIG", clientConfig)
	cwd := t.TempDir()
	t.Chdir(cwd)
	root := &cobra.Command{Use: "crewship", PersistentPreRun: func(cmd *cobra.Command, args []string) { t.Fatal("paths inherited client profile lookup") }}
	root.PersistentFlags().StringVar(&flagFormat, "format", "", "")
	root.AddCommand(newPathsCommand())
	root.SetArgs([]string{"paths", "--format=json"})
	var output bytes.Buffer
	root.SetOut(&output)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var report installationPathsReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	rows := pathRows(report)
	if rows["installation.root"].Path != home || rows["storage.base_path"].Path != filepath.Join(home, "output") {
		t.Fatalf("unexpected paths: %+v", rows)
	}
	pages := rows["storage.page_projects_path"]
	if pages.Enabled == nil || *pages.Enabled || pages.Path != "" {
		t.Fatalf("diagnostic enabled Page projects: %+v", pages)
	}
	for _, path := range []string{home, clientConfig, filepath.Join(cwd, "crewship.db")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("diagnostic created %s: %v", path, err)
		}
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		t.Fatalf("diagnostic wrote cwd: %v, %v", entries, err)
	}
}

func TestPathsConfigurationSourcesAndDatabaseIndependence(t *testing.T) {
	pathsTestEnvironment(t)
	parent := t.TempDir()
	home := filepath.Join(parent, "installation")
	t.Setenv("CREWSHIP_HOME", home)
	configFile := filepath.Join(parent, "server.yaml")
	base := filepath.Join(parent, "yaml-output")
	pages := filepath.Join(parent, "yaml-pages")
	content := "storage:\n  base_path: " + base + "\n  memory_root: ''\n  page_projects_path: " + pages + "\n"
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	withoutDB := pathRows(inspectPaths(t, map[string]string{"config": configFile}))
	if withoutDB["storage.base_path"].Source != "yaml" || withoutDB["storage.memory_root"].Path != "" || withoutDB["storage.page_projects_path"].Source != "yaml" {
		t.Fatalf("YAML source lost: %+v", withoutDB)
	}
	t.Setenv("DATABASE_URL", "file:"+filepath.Join(parent, "external.db"))
	withDB := pathRows(inspectPaths(t, map[string]string{"config": configFile}))
	for _, resource := range []string{"storage.base_path", "storage.log_path", "storage.memory_root", "state.bolt_path", "ipc.socket_path"} {
		if withoutDB[resource].Path != withDB[resource].Path || withoutDB[resource].Source != withDB[resource].Source {
			t.Fatalf("database changed %s: %+v -> %+v", resource, withoutDB[resource], withDB[resource])
		}
	}
	envBase := filepath.Join(parent, "env-output")
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", envBase)
	t.Setenv("CREWSHIP_PAGE_PROJECTS_PATH", filepath.Join(parent, "env-pages"))
	selected := pathRows(inspectPaths(t, map[string]string{"config": configFile, "db": "file:" + filepath.Join(parent, "flag.db")}))
	if selected["storage.base_path"].Path != envBase || selected["storage.base_path"].Source != "env" || selected["database"].Source != "flag" || selected["storage.page_projects_path"].Source != "env" {
		t.Fatalf("override precedence lost: %+v", selected)
	}
	first := selected["storage.log_path"].Path
	t.Chdir(t.TempDir())
	other := pathRows(inspectPaths(t, map[string]string{"config": configFile}))
	if other["storage.log_path"].Path != first {
		t.Fatal("cwd changed derived paths")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("inspection created home: %v", err)
	}
}

func TestPathsReportsLegacyConflictWithoutMovingData(t *testing.T) {
	pathsTestEnvironment(t)
	home := t.TempDir()
	old := filepath.Join(home, "output")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(old, "retained")
	if err := os.WriteFile(artifact, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "new-output")
	t.Setenv("CREWSHIP_STORAGE_BASE_PATH", target)
	report := inspectPaths(t, map[string]string{"data-dir": home})
	if !report.StartupBlocked || !strings.Contains(report.LegacyConflict, old) {
		t.Fatalf("missing legacy diagnostic: %+v", report)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("diagnostic created new store: %v", err)
	}
	if content, err := os.ReadFile(artifact); err != nil || string(content) != "keep" {
		t.Fatalf("legacy data changed: %q, %v", content, err)
	}
}

func TestPathsRedactsDatabasePassword(t *testing.T) {
	pathsTestEnvironment(t)
	report := inspectPaths(t, map[string]string{"data-dir": filepath.Join(t.TempDir(), "unused"), "db": "postgres://operator:private-password@example.test/crewship"})
	row := pathRows(report)["database"]
	if strings.Contains(row.Path, "private-password") || !strings.Contains(row.Path, "xxxxx") {
		t.Fatalf("database password exposed: %s", row.Path)
	}
}

func TestPathsMalformedDatabaseURLDoesNotExposePassword(t *testing.T) {
	if got := redactPathsDatabase("postgres://user:private%password@example.test/db"); strings.Contains(got, "private") {
		t.Fatalf("malformed URL exposed credentials: %s", got)
	}
}
