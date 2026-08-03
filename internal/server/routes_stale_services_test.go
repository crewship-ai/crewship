package server

// A stale services_json must not cost the crew its image.
//
// buildCrewRuntimeConfig returns an error when services_json cannot be decoded
// — despite the comment above it saying a malformed one is "NOT fatal" —
// CompleteCrewConfig propagates it, and crewstart's completion step throws away
// the WHOLE resolved config and starts from the caller's bare {ID, Slug}. So an
// older binary that wrote a service without an `image`, or a healthcheck
// without a `test`, silently downgrades a fully provisioned crew to
// debian:bookworm-slim with no gh, no node and no agent CLI.
//
// That is the exact symptom #1717 was filed for, reappearing inside the change
// that fixes #1717. The crew should lose its sidecars — nothing more.

import (
	"net/http/httptest"
	"testing"
)

// staleServicesJSON is what an older writer could leave behind: a service with
// no image, which crewstart.DecodeServices rejects.
const staleServicesJSON = `[{"name":"redis"}]`

func TestContainerStartKeepsTheProvisionedImageWhenServicesJSONIsStale(t *testing.T) {
	s := newTestServerWithDeps(t)
	rec := &imageRecordingContainer{}
	s.container = rec
	seedProvisionedCrew(t, s, "crew-stale-1", "engineering", "crewship-cache:db6c6fcbdb34")
	mustExec(t, s.db, `UPDATE crews SET services_json = ? WHERE id = 'crew-stale-1'`, staleServicesJSON)

	req := httptest.NewRequest("POST", "/crews/crew-stale-1/container/start", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	cfg, ok := rec.last()
	if !ok {
		t.Fatal("the provider was never asked to start a crew")
	}
	if cfg.CachedImage != "crewship-cache:db6c6fcbdb34" {
		t.Errorf("CachedImage = %q — an undecodable services_json threw away the whole resolved "+
			"config, so the crew started from the default runtime image with none of its provisioned "+
			"toolchain. A stale services column may cost the crew its sidecars, never its image (#1717).",
			cfg.CachedImage)
	}
	if cfg.Slug != "engineering" {
		t.Errorf("Slug = %q, want engineering — the container name derives from it", cfg.Slug)
	}
	if len(cfg.Services) != 0 {
		t.Errorf("Services = %+v, want none — the column could not be decoded", cfg.Services)
	}
}
