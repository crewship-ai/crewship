package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

func TestCrewResponsesWithholdServiceSecrets(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	config := `[{"name":"postgres","image":"postgres:16","env":{"POSTGRES_PASSWORD":"inert-private-marker"}}]`
	if _, err := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug,services_json) VALUES('private-crew',?,'Private','private',?)`, workspace, config); err != nil {
		t.Fatal(err)
	}
	h := NewCrewHandler(db, newTestLogger())
	for _, role := range []string{"VIEWER", "MEMBER", "MANAGER", "ADMIN", "OWNER"} {
		t.Run(role, func(t *testing.T) {
			user := seedMemberWithCapabilities(t, db, workspace, role, `["chat"]`, "private-"+strings.ToLower(role))
			for _, handler := range []http.HandlerFunc{h.List, h.Get} {
				req := httptest.NewRequest("GET", "/api/v1/crews?workspace_id="+workspace, nil)
				req = req.WithContext(withUser(req.Context(), &AuthUser{ID: user}))
				req.SetPathValue("crewId", "private-crew")
				rec := httptest.NewRecorder()
				(&AuthMiddleware{db: db}).RequireWorkspace(handler).ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Fatalf("status=%d", rec.Code)
				}
				if strings.Contains(rec.Body.String(), "inert-private-marker") {
					t.Fatal("private value escaped crew response")
				}
				if !strings.Contains(rec.Body.String(), serviceconfig.Redacted) {
					t.Fatal("missing explicit redaction marker")
				}
			}
		})
	}
	// Serialization protects mutation responses as well, without mutating the
	// original stored value used by the service runtime.
	response := crewResponse{ServicesJSON: (*publicServiceConfig)(&config)}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "inert-private-marker") {
		t.Fatal("serialization leaked")
	}
	if !strings.Contains(string(*response.ServicesJSON), "inert-private-marker") {
		t.Fatal("runtime configuration mutated")
	}
	// A stale/older client must not regenerate or erase a service password.
	for _, replacement := range []string{"[]", `[{"name":"postgres","image":"postgres:16","env":{"POSTGRES_PASSWORD":"different"}}]`} {
		body, _ := json.Marshal(map[string]string{"services_json": replacement})
		rec := covCruDoUpdate(h, "private-crew", owner, workspace, "OWNER", string(body))
		if rec.Code != http.StatusConflict {
			t.Fatalf("unsafe replace status=%d", rec.Code)
		}
	}
	var stored string
	if err := db.QueryRow("SELECT services_json FROM crews WHERE id='private-crew'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != config {
		t.Fatal("rejected update changed stored configuration")
	}
}

func TestCrewPrivateConfigPreservesEnvelopeWarnings(t *testing.T) {
	config := publicServiceConfig(`[{"command":["inert-private-marker"]}]`)
	response := struct {
		crewResponse
		Warnings []string `json:"warnings"`
	}{crewResponse: crewResponse{ServicesJSON: &config}, Warnings: []string{"keep-this-warning"}}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "inert-private-marker") || !strings.Contains(string(body), "keep-this-warning") {
		t.Fatal("redaction must preserve sibling envelope fields")
	}
}
