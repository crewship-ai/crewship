package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/pagebuild"
)

func validatePageProjectPath(projects, crews string) error {
	if projects == "" {
		return nil
	}
	if !filepath.IsAbs(projects) {
		return fmt.Errorf("storage.page_projects_path must be absolute")
	}
	p, err := resolveExistingParent(projects)
	if err != nil {
		return err
	}
	c, err := resolveExistingParent(crews)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{p, c}, {c, p}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return err
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return fmt.Errorf("storage.page_projects_path must not overlap crew storage.base_path")
		}
	}
	return nil
}

// Resolve symlinked existing parents even when the final directory is created
// lazily. These are trusted operator paths, never paths from an uploaded bundle.
func resolveExistingParent(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(abs)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", err
		}
		suffix = append(suffix, filepath.Base(abs))
		abs = parent
	}
}

func validatePageBuildImage(image, projects string) error {
	if image == "" {
		return nil
	}
	if projects == "" {
		return errors.New("Page builds require storage.page_projects_path")
	}
	return pagebuild.ValidateImage(image)
}

func validatePageRuntime(runtime, studio string, development ...bool) error {
	if runtime == "" {
		return nil
	}
	return pagebuild.ValidateRuntimeOriginForDevelopment(runtime, studio, len(development) > 0 && development[0])
}

// PageStudioOrigin is the browser parent allowed to initialize a Page iframe.
// An explicit origin prevents public reverse proxies from becoming the transport
// for internal token sync and agent IPC, which still use Auth.NextjsURL.
func (c *Config) PageStudioOrigin() string {
	if c.Storage.PageStudioOrigin != "" {
		return c.Storage.PageStudioOrigin
	}
	return c.Auth.NextjsURL
}
