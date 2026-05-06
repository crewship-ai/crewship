package connectors

import (
	"embed"
	"errors"
	"io/fs"
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
//
// Internal fields are unexported and intentionally unused by the stub
// — the implementer fills LoadAll/LoadByID/List/Len bodies and then
// these become live state. Marked nolint:unused to keep the linter
// from blocking the TDD scaffold commit.
type Catalog struct {
	manifests map[string]*Manifest //nolint:unused // populated by LoadAll once impl lands
	order     []string             //nolint:unused // populated by LoadAll once impl lands
}

// LoadAll walks the given fs.FS, parses every *.yaml file as a
// Manifest, runs Validate, and returns a Catalog. Files that fail to
// parse or validate are skipped with their error appended to the
// returned []error so callers can surface partial-load problems
// without blocking the rest of the catalog. A nil error slice means
// the whole catalog loaded cleanly.
//
// Pass FixturesFS to load the shipped fixtures; pass an os.DirFS or
// a test-built fstest.MapFS to load alternate sources.
//
// TDD STUB — returns an empty catalog so accidental production calls
// fail gracefully (empty results, not panic).
func LoadAll(filesystem fs.FS) (*Catalog, []error) {
	return &Catalog{}, nil
}

// LoadByID returns one manifest by id, or ErrConnectorNotFound.
//
// TDD STUB — always returns ErrConnectorNotFound until wired up.
func (c *Catalog) LoadByID(id string) (*Manifest, error) {
	return nil, ErrConnectorNotFound
}

// List returns all manifests in stable (insertion) order. Used by the
// API layer to drive the catalog tile grid.
//
// TDD STUB — returns an empty (non-nil) slice until wired up.
func (c *Catalog) List() []*Manifest {
	return []*Manifest{}
}

// Len reports the number of valid manifests in the catalog.
//
// TDD STUB — returns 0 until wired up.
func (c *Catalog) Len() int {
	return 0
}
