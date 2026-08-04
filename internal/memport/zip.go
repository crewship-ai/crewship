package memport

import (
	"archive/zip"
	"fmt"
	"io"
	"path"

	"gopkg.in/yaml.v3"
)

// WriteOKFZip streams the same bundle ExportOKF writes to a directory,
// as a zip.
//
// It exists so the dashboard can offer a download without knowing what
// an OKF bundle looks like. A TypeScript copy of the frontmatter and the
// manifest would drift the first time either changes, and the format is
// the whole point of the feature — so the browser gets a finished
// archive and the layout stays in one place.
//
// Entry order follows docs, which ExportOKF already fixes, so two
// downloads of unchanged memory produce the same archive.
func WriteOKFZip(w io.Writer, docs []Doc, scope string) error {
	zw := zip.NewWriter(w)
	man := Manifest{
		Format:    "okf",
		Version:   "0.1",
		Generator: "crewship",
		Scope:     scope,
		Documents: make([]ManifestEntry, 0, len(docs)),
	}

	for _, d := range docs {
		if !validRelPath(d.RelPath) {
			return fmt.Errorf("memport: refusing %q in a bundle", d.RelPath)
		}
		body, err := renderFrontmatter(frontmatter{
			Type:         string(d.Tier),
			Scope:        string(d.Scope),
			Title:        d.Title,
			Tags:         d.Tags,
			CrewshipPath: d.RelPath,
		}, d.Body)
		if err != nil {
			return fmt.Errorf("memport: render %s: %w", d.RelPath, err)
		}
		f, err := zw.Create(path.Clean(d.RelPath))
		if err != nil {
			return fmt.Errorf("memport: zip entry %s: %w", d.RelPath, err)
		}
		if _, err := f.Write(body); err != nil {
			return fmt.Errorf("memport: zip write %s: %w", d.RelPath, err)
		}
		man.Documents = append(man.Documents, ManifestEntry{
			Path:    d.RelPath,
			Tier:    string(d.Tier),
			Scope:   string(d.Scope),
			Title:   d.Title,
			Tags:    d.Tags,
			Sources: d.Sources,
		})
	}

	buf, err := yaml.Marshal(man)
	if err != nil {
		return fmt.Errorf("memport: render manifest: %w", err)
	}
	f, err := zw.Create(manifestName)
	if err != nil {
		return fmt.Errorf("memport: zip manifest: %w", err)
	}
	if _, err := f.Write(buf); err != nil {
		return fmt.Errorf("memport: zip manifest: %w", err)
	}
	return zw.Close()
}
