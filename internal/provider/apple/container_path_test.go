package apple

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestContainerPathInspectPayload(t *testing.T) {
	data, err := os.ReadFile("testdata/inspect_crew_container.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []containerJSON
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("unexpected inspect entries: %d", len(entries))
	}
	path := entries[0].ConfiguredPath()
	if !strings.HasPrefix(path, "/home/agent/.local/bin:") || strings.Contains(path, "NVM_DIR=") {
		t.Fatalf("expected only PATH, got %q", path)
	}
	if (containerJSON{}).ConfiguredPath() != "" {
		t.Fatal("invented absent metadata")
	}
}
