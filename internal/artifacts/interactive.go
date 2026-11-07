package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// InteractiveMarkerPath marks a Project whose HTML may run its own scripts in
// the isolated application runtime. Its data collections stay declared by
// dashboard.json, so reads and writes use the existing data authority.
const InteractiveMarkerPath = "interactive.json"

// MaxInteractiveMarkerBytes bounds the marker; it carries no settings today.
const MaxInteractiveMarkerBytes = 1024

// ErrNotInteractive reports a Project without the interactive marker.
var ErrNotInteractive = errors.New("artifact project is not interactive")

// ParseInteractiveArtifact returns the collections an interactive Project may
// reach. The marker is closed: unknown fields are refused rather than read as
// capabilities, so a later schema cannot be granted by an old server.
func ParseInteractiveArtifact(files map[string]VersionFile) ([]string, error) {
	marker, ok := files[InteractiveMarkerPath]
	if !ok {
		return nil, ErrNotInteractive
	}
	raw := marker.Content
	if marker.MediaType != "application/json" || len(raw) > MaxInteractiveMarkerBytes || !validDataObject(raw) || !canonicalDataFields(raw, "schema_version") {
		return nil, ErrVersionInvalid
	}
	var declaration struct {
		SchemaVersion int `json:"schema_version"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&declaration) != nil || declaration.SchemaVersion != 1 {
		return nil, ErrVersionInvalid
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrVersionInvalid
	}
	dashboard, ok := files["dashboard.json"]
	if !ok || dashboard.MediaType != "application/json" {
		return nil, ErrVersionInvalid
	}
	return ParseDashboardCollections(dashboard.Content)
}
