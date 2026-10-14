// Package pageprofile embeds the same dependency lock and starter used by the
// isolated build image. API/CLI producers cannot drift from the compiler profile.
package pageprofile

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/pages"
)

//go:embed package.json pnpm-lock.yaml pnpm-workspace.yaml starter sdk.ts build.mjs Dockerfile
var files embed.FS

// ImageLabel marks a tools image built for one profile. Its value is
// Fingerprint(), so a server finds only the image matching its own release.
const ImageLabel = "ai.crewship.pages.profile"

// buildContextFiles is everything the Dockerfile COPYs, plus the Dockerfile.
var buildContextFiles = []string{"Dockerfile", "package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "build.mjs", "sdk.ts"}

// WriteBuildContext writes the tools image build context embedded in this
// binary, so an installation needs no source checkout to build its compiler.
func WriteBuildContext(dir string) error {
	for _, name := range buildContextFiles {
		data, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func Source() *pages.SourceProject {
	p := &pages.SourceProject{Format: pages.SourceProjectFormat, Runtime: pages.SourceProjectRuntime}
	for _, name := range []string{"package.json", "pnpm-lock.yaml"} {
		data, _ := files.ReadFile(name)
		p.Files = append(p.Files, pages.ProjectFile{Path: name, Encoding: "utf8", Content: string(data)})
	}
	_ = fs.WalkDir(files, "starter", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		p.Files = append(p.Files, pages.ProjectFile{Path: strings.TrimSuffix(strings.TrimPrefix(name, "starter/"), ".tmpl"), Encoding: "utf8", Content: string(data)})
		return nil
	})
	return p
}

// SDKSource gives authors the exact API shipped in this build profile.
func SDKSource() string {
	data, _ := files.ReadFile("sdk.ts")
	return string(data)
}
