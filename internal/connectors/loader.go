package connectors

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

// FixturesFS is the package-internal embedded filesystem holding the
// shipped manifest fixtures. It is exported (lowercase, package-only)
// for tests; the public API is LoadAll / LoadByID.
//
//go:embed fixtures/*.yaml
var FixturesFS embed.FS

// ErrConnectorNotFound is returned by LoadByID when the requested id
// has no matching manifest in the embedded catalog. The error is
// distinct from ParseManifest errors so API handlers can return 404
// rather than 500 when a user requests an unknown connector.
var ErrConnectorNotFound = errors.New("connectors: connector not found")

// Catalog is the loaded set of valid manifests, keyed by ID. LoadAll
// constructs and returns a Catalog; tests may construct one directly.
type Catalog struct {
	manifests map[string]*Manifest
	order     []string
}

// LoadAll walks the given fs.FS, parses every fixtures/*.yaml file as
// a Manifest, runs Validate, and returns a Catalog. Files that fail
// to parse or validate are skipped with their error appended to the
// returned []error so callers can surface partial-load problems
// without blocking the rest of the catalog. A nil error slice means
// the whole catalog loaded cleanly.
//
// The walk is intentionally flat — only fixtures/*.yaml is loaded.
// Nested directories (fixtures/vendors/foo.yaml) are skipped silently
// because the package's embed glob doesn't include them either, and
// silently honoring nesting would let a fixture sneak past the
// shipping-set audit.
//
// Pass FixturesFS to load the shipped fixtures; pass an os.DirFS or
// a test-built fstest.MapFS to load alternate sources.
func LoadAll(filesystem fs.FS) (*Catalog, []error) {
	cat := &Catalog{manifests: make(map[string]*Manifest)}
	if filesystem == nil {
		return cat, nil
	}

	matches, err := fs.Glob(filesystem, "fixtures/*.yaml")
	if err != nil {
		return cat, []error{fmt.Errorf("connectors: glob fixtures: %w", err)}
	}
	// Glob order is implementation-defined; sort so List() is stable
	// across builds + across machines.
	sort.Strings(matches)

	var loadErrs []error
	for _, p := range matches {
		// Defense in depth — Glob shouldn't return nested paths for
		// `fixtures/*.yaml` but a future libc rewrite could surprise
		// us. Explicit dir check pins the contract.
		if path.Dir(p) != "fixtures" {
			continue
		}
		data, err := fs.ReadFile(filesystem, p)
		if err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("%s: read: %w", p, err))
			continue
		}
		m, err := ParseManifest(data)
		if err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("%s: parse: %w", p, err))
			continue
		}
		if err := m.Validate(); err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("%s: validate: %w", p, err))
			continue
		}
		if _, dup := cat.manifests[m.ID]; dup {
			loadErrs = append(loadErrs, fmt.Errorf("%s: duplicate id %q", p, m.ID))
			continue
		}
		cat.manifests[m.ID] = m
		cat.order = append(cat.order, m.ID)
	}
	return cat, loadErrs
}

// LoadByID returns one manifest by id, or ErrConnectorNotFound.
func (c *Catalog) LoadByID(id string) (*Manifest, error) {
	if c == nil || c.manifests == nil {
		return nil, ErrConnectorNotFound
	}
	m, ok := c.manifests[id]
	if !ok {
		return nil, ErrConnectorNotFound
	}
	return m, nil
}

// List returns all manifests in stable (insertion) order. Used by the
// API layer to drive the catalog tile grid.
func (c *Catalog) List() []*Manifest {
	if c == nil {
		return []*Manifest{}
	}
	out := make([]*Manifest, 0, len(c.order))
	for _, id := range c.order {
		if m, ok := c.manifests[id]; ok {
			out = append(out, m)
		}
	}
	return out
}

// Len reports the number of valid manifests in the catalog.
func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.manifests)
}
