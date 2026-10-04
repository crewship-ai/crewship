package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestCrewAPIPersistsNativeLockEnvelope(t *testing.T) {
	h, db, user, workspace := covCruNewCrew(t)
	// Native lock bundles may contain large auxiliary lock files. Escaping the
	// JSON-valued mise_config once more must not trip the old 1 MiB body reader.
	content := "#" + strings.Repeat("\t", (128<<10)-1)
	files := map[string]string{"mise.lock": content}
	for _, name := range []string{"a", "b", "c"} {
		files[".mise/locks/node/"+name] = content
	}
	cfg := devcontainer.MiseConfig{Tools: map[string]string{"node": "22"}, Lock: &devcontainer.MiseLockBundle{SchemaVersion: 1, Files: files}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"name": "Locked tools", "slug": "locked-tools", "mise_config": string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 1<<20 {
		t.Fatal("fixture must exceed legacy body reader")
	}
	created := covCruDoCreate(h, user, workspace, "OWNER", string(body))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var id, saved string
	if err := db.QueryRow("SELECT id,mise_config FROM crews WHERE workspace_id=? AND slug=?", workspace, "locked-tools").Scan(&id, &saved); err != nil {
		t.Fatal(err)
	}
	if saved != string(raw) {
		t.Fatal("created lock was truncated or changed")
	}
	cfg.Tools["node"] = "22.0.0"
	raw, _ = json.Marshal(cfg)
	body, _ = json.Marshal(map[string]any{"mise_config": string(raw)})
	updated := covCruDoUpdate(h, id, user, workspace, "OWNER", string(body))
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updated.Code, updated.Body.String())
	}
	if err := db.QueryRow("SELECT mise_config FROM crews WHERE id=?", id).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != string(raw) {
		t.Fatal("updated lock was truncated or changed")
	}
	for _, invalid := range []string{
		`{"tools":{"node":"22"},"lock":{"schema_version":1,"files":{"mise.lock":"x","../escape":"x"}}}`,
		`{"tools":{"node":"22"},"env":{"EXTRA":"` + strings.Repeat("x", 11<<10) + `"},"lock":{"schema_version":1,"files":{"mise.lock":"x"}}}`,
	} {
		badBody, _ := json.Marshal(map[string]any{"mise_config": invalid})
		rejected := covCruDoUpdate(h, id, user, workspace, "OWNER", string(badBody))
		if rejected.Code != http.StatusBadRequest {
			t.Fatalf("invalid update: %d", rejected.Code)
		}
		var unchanged string
		if err := db.QueryRow("SELECT mise_config FROM crews WHERE id=?", id).Scan(&unchanged); err != nil {
			t.Fatal(err)
		}
		if unchanged != saved {
			t.Fatal("rejected input changed stored lock")
		}
	}

}
