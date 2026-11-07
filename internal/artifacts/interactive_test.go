package artifacts

import (
	"errors"
	"slices"
	"testing"
)

func TestParseInteractiveArtifact(t *testing.T) {
	html := VersionFile{Content: []byte("<h1 id=poll>Poll</h1><script>crewship.data.list()</script>"), MediaType: "text/html"}
	dashboard := VersionFile{Content: []byte(`{"schema_version":1,"collections":["votes","notes"]}`), MediaType: "application/json"}
	marker := func(content string) VersionFile {
		return VersionFile{Content: []byte(content), MediaType: "application/json"}
	}
	cases := []struct {
		name        string
		files       map[string]VersionFile
		collections []string
		err         error
	}{
		{"marker and collections", map[string]VersionFile{"index.html": html, "dashboard.json": dashboard, InteractiveMarkerPath: marker(`{"schema_version":1}`)}, []string{"votes", "notes"}, nil},
		{"marker with whitespace", map[string]VersionFile{"index.html": html, "dashboard.json": dashboard, InteractiveMarkerPath: marker(" {\n \"schema_version\": 1 }\n")}, []string{"votes", "notes"}, nil},
		{"plain dashboard is not interactive", map[string]VersionFile{"index.html": html, "dashboard.json": dashboard}, nil, ErrNotInteractive},
		{"marker without collections", map[string]VersionFile{"index.html": html, InteractiveMarkerPath: marker(`{"schema_version":1}`)}, nil, ErrVersionInvalid},
		{"wrong schema version", map[string]VersionFile{"dashboard.json": dashboard, InteractiveMarkerPath: marker(`{"schema_version":2}`)}, nil, ErrVersionInvalid},
		{"unknown marker field", map[string]VersionFile{"dashboard.json": dashboard, InteractiveMarkerPath: marker(`{"schema_version":1,"permissions":["cookies"]}`)}, nil, ErrVersionInvalid},
		{"duplicate marker field", map[string]VersionFile{"dashboard.json": dashboard, InteractiveMarkerPath: marker(`{"schema_version":1,"schema_version":1}`)}, nil, ErrVersionInvalid},
		{"trailing value", map[string]VersionFile{"dashboard.json": dashboard, InteractiveMarkerPath: marker(`{"schema_version":1}{}`)}, nil, ErrVersionInvalid},
		{"wrong media type", map[string]VersionFile{"dashboard.json": dashboard, InteractiveMarkerPath: {Content: []byte(`{"schema_version":1}`), MediaType: "text/plain"}}, nil, ErrVersionInvalid},
		{"oversized marker", map[string]VersionFile{"dashboard.json": dashboard, InteractiveMarkerPath: marker(`{"schema_version":1}` + string(make([]byte, MaxInteractiveMarkerBytes)))}, nil, ErrVersionInvalid},
		{"invalid collections", map[string]VersionFile{"dashboard.json": marker(`{"schema_version":1,"collections":[]}`), InteractiveMarkerPath: marker(`{"schema_version":1}`)}, nil, ErrVersionInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseInteractiveArtifact(tc.files)
			if !errors.Is(err, tc.err) || !slices.Equal(got, tc.collections) {
				t.Fatalf("got %v %v, want %v %v", got, err, tc.collections, tc.err)
			}
		})
	}
}
