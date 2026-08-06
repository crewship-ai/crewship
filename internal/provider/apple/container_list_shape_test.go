package apple

import (
	"encoding/json"
	"testing"
)

// `container list --all --format json` reports status as an OBJECT — networks,
// startedDate and the actual state under `state` — not as a string. The entry
// struct declared `Status string`, so decoding the whole list failed with a
// type error, findContainer reported "not found" for a container that was
// plainly running, and EnsureCrewRuntime fell through to create it again:
//
//	Error: container already exists: crewship-1-team-quality-…
//
// Same shape of mistake as the image list — a struct written against an
// imagined payload — and it hid the same way, behind an error path that looks
// like a plain absence (#1779).
const appleContainerListJSON = `[{
  "id":"crewship-1-team-quality-abc",
  "status":{"state":"running","startedDate":"2026-08-06T15:41:43Z",
            "networks":[{"hostname":"h","ipv4Address":"192.168.65.2/24"}]},
  "configuration":{"id":"crewship-1-team-quality-abc",
                   "image":{"reference":"crewship-cache:66e2"}}
}]`

func TestContainerListEntry_DecodesTheRealPayload(t *testing.T) {
	var entries []containerListEntry
	if err := json.Unmarshal([]byte(appleContainerListJSON), &entries); err != nil {
		t.Fatalf("the real CLI payload must decode: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
	if got := entries[0].Configuration.ID; got != "crewship-1-team-quality-abc" {
		t.Errorf("configuration.id = %q", got)
	}
	if got := entries[0].State(); got != "running" {
		t.Errorf("State() = %q, want running — a running container read as anything else gets recreated", got)
	}
}

func TestContainerListEntry_MissingStateIsEmptyNotAFailure(t *testing.T) {
	var entries []containerListEntry
	if err := json.Unmarshal([]byte(`[{"configuration":{"id":"x"}}]`), &entries); err != nil {
		t.Fatalf("a payload without status must still decode: %v", err)
	}
	if got := entries[0].State(); got != "" {
		t.Errorf("State() = %q, want empty", got)
	}
}
