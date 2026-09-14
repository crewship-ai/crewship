package pageprofile

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

func TestProfileRejectsUnsupportedPathsBeforeBuild(t *testing.T) {
	for _, name := range []string{"src/čísla.tsx", "src/my file.tsx", "src/.keep", "vite.config.ts", "src/PostCSS.config.js"} {
		t.Run(name, func(t *testing.T) {
			p := Source()
			p.Files = append(p.Files, pages.ProjectFile{Path: name, Encoding: "utf8", Content: "export {}"})
			if err := ValidateSourcePaths(p); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("missing path diagnostic: %v", err)
			}
		})
	}
	if err := ValidateDependencies(Source()); err != nil {
		t.Fatal(err)
	}
}

func TestProfileDependencyDiagnosticsAndContract(t *testing.T) {
	for _, name := range []string{"package.json", "pnpm-lock.yaml"} {
		p := Source()
		for i := range p.Files {
			if p.Files[i].Path == name {
				p.Files[i].Content = "{}"
			}
		}
		if err := ValidateDependencies(p); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	contract := Contract()
	if contract["runtime"] != pages.SourceProjectRuntime || contract["dependencies"] == nil || len(contract["sdk_sha256"].(string)) != 64 {
		t.Fatal(contract)
	}
}
