package pipeline

import (
	"os"
	"strings"
	"testing"
)

func TestMigrationGuideDescribesRetainedCLIPaths(t *testing.T) {
	data, err := os.ReadFile("../../docs/guides/migrations.mdx")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(data), "20261119180903_waitpoint_callback_token_hash")
	if !found {
		t.Fatal("missing migration guide section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	if !strings.Contains(section, "cli_token_uses.path") || strings.Contains(section, "No trace of the old value is left") {
		t.Fatal("guide must explain retention of historical ADMIN CLI paths")
	}
}
