package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// agent_files remembers which pages an agent was given; it grants nothing by itself. Every SQL
// statement of the server that reads it is bound three ways: to the workspace, to the agent, and
// to rows not taken back (removed_at IS NULL). Today no server path reads it: a new one must keep
// all three.
func TestAgentFilesReadsKeepTheirThreeGuards(t *testing.T) {
	read := regexp.MustCompile(`(?is)\b(FROM|JOIN)\s+agent_files\b`)
	statement := regexp.MustCompile("(?s)`[^`]*`")
	for _, dir := range []string{".", "../database", "../chatbridge", "../sidecar"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range statement.FindAllString(string(src), -1) {
				if !read.MatchString(q) {
					continue
				}
				lower := strings.ToLower(q)
				for _, guard := range []string{"workspace_id", "agent_id", "removed_at is null"} {
					if !strings.Contains(lower, guard) {
						t.Errorf("%s reads agent_files without %s: %s", file, guard, q)
					}
				}
			}
		}
	}
}
