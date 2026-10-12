package dockerfilesources

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// frontendCopies expands actual context COPY sources rather than maintaining
// a second allowlist. Every tracked TS input admitted by the current tsconfig
// must survive the narrowed context, including non-application type checks.
func frontendCopies(t *testing.T, root string) []string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	active := false
	var sources []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "FROM ") {
			active = strings.HasSuffix(line, " AS frontend")
			continue
		}
		if !active || !strings.HasPrefix(line, "COPY ") {
			continue
		}
		fields := strings.Fields(line)
		if strings.HasPrefix(fields[1], "--from") {
			continue
		}
		sources = append(sources, fields[1:len(fields)-1]...)
	}
	if len(sources) == 0 {
		t.Fatal("frontend COPY inputs not found")
	}
	return sources
}

func copiedBy(sources []string, file string) bool {
	for _, source := range sources {
		source = strings.TrimSuffix(source, "/")
		if file == source || strings.HasPrefix(file, source+"/") {
			return true
		}
		match, _ := filepath.Match(source, file)
		if match {
			return true
		}
	}
	return false
}

func excludedTypeScript(t *testing.T, file string, excludes []string) bool {
	t.Helper()
	for _, pattern := range excludes {
		switch pattern {
		case "**/__tests__/**/*":
			if strings.Contains("/"+file, "/__tests__/") {
				return true
			}
		case "**/*.test.ts":
			if strings.HasSuffix(file, ".test.ts") {
				return true
			}
		case "**/*.test.tsx":
			if strings.HasSuffix(file, ".test.tsx") {
				return true
			}
		default:
			if strings.ContainsAny(pattern, "*?[") {
				t.Fatalf("new tsconfig exclusion %q needs source-contract support", pattern)
			}
			if file == pattern || strings.HasPrefix(file, strings.TrimSuffix(pattern, "/")+"/") {
				return true
			}
		}
	}
	return false
}

func TestFrontendStagePreservesTypecheckAndBuildInputs(t *testing.T) {
	root := filepath.Join("..", "..")
	sources := frontendCopies(t, root)
	raw, err := os.ReadFile(filepath.Join(root, "tsconfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Include []string
		Exclude []string
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	includes := map[string]bool{}
	for _, pattern := range config.Include {
		includes[pattern] = true
	}
	if !includes["**/*.ts"] || !includes["**/*.tsx"] {
		t.Fatal("production tsconfig include changed; review frontend source contract")
	}
	output, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatal(err)
	}
	jsonImports := regexp.MustCompile(`@/([^"'\s]+\.json)`)
	for _, file := range strings.Split(string(output), "\x00") {
		if !strings.HasSuffix(file, ".ts") && !strings.HasSuffix(file, ".tsx") {
			continue
		}
		if excludedTypeScript(t, file, config.Exclude) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range jsonImports.FindAllSubmatch(content, -1) {
			dependency := string(match[1])
			if !copiedBy(sources, dependency) {
				t.Errorf("frontend COPY omits JSON contract %s referenced by %s", dependency, file)
			}
		}
		if !copiedBy(sources, file) {
			t.Errorf("frontend COPY omits existing TypeScript input %s", file)
		}
	}
	// Non-TS build boundaries: dependency graph, generated Prisma types,
	// offline PDF assets and the locked-tree legal inventory.
	for _, file := range []string{
		"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", ".npmrc", "next.config.ts", "tsconfig.json",
		"postcss.config.mjs", "prisma.config.ts", "prisma/schema.prisma", "sentry.client.config.ts",
		"sentry.server.config.ts", "sentry.edge.config.ts", "public/logo.svg",
		"scripts/prepare-pdf-assets.mjs", "scripts/gen-frontend-licenses.mjs",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(file))); err != nil {
			t.Fatal(err)
		}
		if !copiedBy(sources, file) {
			t.Errorf("frontend COPY omits required build input %s", file)
		}
	}
	for _, source := range sources {
		if source == "." || source == "./" || source == "*" {
			t.Fatal("frontend COPY includes whole checkout")
		}
	}
	for _, file := range []string{"internal/api/api.go", "cmd/crewship/main.go", "config/embed.go", "config/rate-limits.yml",
		"schemas/embed.go", "scripts/claim-issue.sh", "tools/gen-licenses/main.go", "docs/README.md"} {
		if copiedBy(sources, file) {
			t.Errorf("backend/unrelated input %s invalidates frontend source cache", file)
		}
	}
}
