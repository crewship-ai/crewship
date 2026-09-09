package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

func TestCrewPrivateConfigurationEncryptedAtRest(t *testing.T) {
	ensureEncryptionKey(t)
	h, db, owner, ws := covCruNewCrew(t)
	const raw = `[{"name":"db","image":"postgres:16","env":{"POSTGRES_PASSWORD":"inert-private-canary"}}]`
	body, err := json.Marshal(map[string]string{"name": "Private crew", "slug": "private-encrypted", "services_json": raw})
	if err != nil {
		t.Fatal(err)
	}
	response := covCruDoCreate(h, owner, ws, "OWNER", string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "inert-private-canary") {
		t.Fatal("create response leaked")
	}
	var stored, id string
	if err := db.QueryRow("SELECT id, services_json FROM crews WHERE slug='private-encrypted'").Scan(&id, &stored); err != nil {
		t.Fatal(err)
	}
	if stored == raw || strings.Contains(stored, "inert-private-canary") {
		t.Fatal("plaintext persisted")
	}
	services, err := crewstart.DecodeServices(stored, nil)
	if err != nil || len(services) != 1 {
		t.Fatal("encrypted runtime decode failed")
	}
	if got := parseDatastores(stored); len(got) != 1 || got[0].Type != "postgres" {
		t.Fatal("encrypted service inventory lost")
	}
	if plain, err := serviceconfig.Open(stored); err != nil || plain != raw {
		t.Fatal("service bytes changed")
	}
	// Exact known configuration is accepted; the ciphertext must never be
	// accepted as a client-authored configuration or copied across crews.
	for _, config := range []string{stored, serviceconfig.Redacted} {
		patch, _ := json.Marshal(map[string]string{"services_json": config})
		rec := covCruDoUpdate(h, id, owner, ws, "OWNER", string(patch))
		if rec.Code != http.StatusConflict {
			t.Fatalf("ciphertext replay status %d", rec.Code)
		}
	}
}
