// Package pageprofile embeds the same dependency lock and starter used by the
// isolated build image. API/CLI producers cannot drift from the compiler profile.
package pageprofile

import (
	"embed"
	"io/fs"
	"strings"

	"github.com/crewship-ai/crewship/internal/pages"
)

//go:embed package.json pnpm-lock.yaml starter sdk.ts build.mjs
var files embed.FS

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
