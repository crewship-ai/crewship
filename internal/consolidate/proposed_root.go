package consolidate

import (
	"fmt"
	"os"
	"path/filepath"
)

func openProposedRoot(trustedRoot, outputDir string) (*os.Root, error) {
	var outputRoot *os.Root
	if trustedRoot == "" {
		root, err := os.OpenRoot(outputDir)
		if err != nil {
			return nil, fmt.Errorf("open output root: %w", err)
		}
		outputRoot = root
	} else {
		rel, err := filepath.Rel(trustedRoot, outputDir)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("output directory escapes trusted root")
		}
		root, err := os.OpenRoot(trustedRoot)
		if err != nil {
			return nil, fmt.Errorf("open trusted root: %w", err)
		}
		outputRoot, err = root.OpenRoot(rel)
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("open output beneath trusted root: %w", err)
		}
	}
	proposedRoot, err := outputRoot.OpenRoot(".proposed")
	_ = outputRoot.Close()
	if err != nil {
		return nil, fmt.Errorf("open proposed root: %w", err)
	}
	return proposedRoot, nil
}
