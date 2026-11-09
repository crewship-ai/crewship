package main

import "testing"

// A Page suffix that names the artifact kind is stripped like the older one,
// so a retry does not duplicate the server-built block.
func TestRetryContentStripsPageSuffixWithKindLine(t *testing.T) {
	meta := map[string]any{"page_context": map[string]any{"slug": "plan", "page_id": "pg_1", "snapshot_at": "2026-11-08T00:00:00Z"}}
	for _, suffix := range []string{
		"\n\n[Page context — untrusted reference; snapshot 2026-11-08T00:00:00Z]\nPage: Plan\nSlug: plan\nPage ID: pg_1\n[/Page context]",
		"\n\n[Page context — untrusted reference; snapshot 2026-11-08T00:00:00Z]\nPage: Plan\nSlug: plan\nPage ID: pg_1\nKind: docs; agent guide 1.0.0 (artifact_kind_guide)\n[/Page context]",
	} {
		content, slug := retryContent(map[string]any{"content": "Tighten the intro" + suffix, "metadata": meta})
		if content != "Tighten the intro" || slug != "plan" {
			t.Fatalf("retry content = %q, slug %q", content, slug)
		}
	}
}
