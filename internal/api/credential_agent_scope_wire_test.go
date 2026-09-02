package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The agent_ids hop, end to end (#2052).
//
// Ownership crosses four types on its way to the sidecar's CredStore:
//
//	deliveredCredential.GrantedAgentIDs   (internal/api)
//	  → mcpCredEntry.AgentIDs             `json:"agent_ids,omitempty"`
//	  → chatbridge credentialResponse     (decodes that JSON)
//	  → orchestrator.Credential.AgentIDs
//	  → sidecarCred.AgentIDs              (pinned by TestSidecarCredWireTags)
//
// Only the last link was pinned. Every other one is a plain struct assignment
// that fails OPEN when it is dropped: the field arrives empty, empty means
// crew-wide, and the whole change silently reverts to serving any member's
// credential to any member — with every test in the tree still green. This file
// pins the two links on this side of the socket; the chatbridge decode is
// pinned in internal/chatbridge. Same guard #1373 built for LeaseExpiresAt
// (credential_lease_boot_payload_test.go), for the same reason.
func TestAgentScope_BootResolverCarriesGrantedAgentIDs(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := covCfgHandler(db)

	const (
		crewID = "aws-crew"
		agentA = "aws-agent-a"
		agentB = "aws-agent-b"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'aws-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'aws-a')`, agentA, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'aws-b')`, agentB, crewID, wsID)

	// Scoped to A alone: the shape #2052 is about.
	seedCredentialEnc(t, db, wsID, userID, "aws-scoped", "aws-scoped",
		`{"baseURL":"https://a.example/v1","apiKey":"sk-a"}`)
	execOrFatal(t, db, `UPDATE credentials SET provider = 'OPENAI_COMPAT', type = 'API_KEY' WHERE id = 'aws-scoped'`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('aws-ac', ?, 'aws-scoped', 'COMPAT_A', 0, datetime('now'))`, agentA)

	// Crew-linked: reaches everyone, must carry NO agent ids so the payload
	// stays byte-identical to its pre-#2052 self.
	seedCredentialEnc(t, db, wsID, userID, "aws-crewlink", "CREW_KEY", "tok-crew")
	execOrFatal(t, db, `UPDATE credentials SET provider = 'ANTHROPIC', type = 'API_KEY' WHERE id = 'aws-crewlink'`)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('aws-crewlink', ?)`, crewID)

	creds, err := h.resolveAgentCredentials(httptest.NewRequest("GET", "/", nil), agentA)
	if err != nil {
		t.Fatalf("resolveAgentCredentials: %v", err)
	}

	byID := map[string]mcpCredEntry{}
	for _, c := range creds {
		byID[c.ID] = c
	}

	scoped, ok := byID["aws-scoped"]
	if !ok {
		t.Fatal("the agent-scoped credential was not delivered")
	}
	if strings.Join(scoped.AgentIDs, ",") != agentA {
		t.Errorf("AgentIDs = %v, want [%s]: the resolver dropped ownership, so the "+
			"sidecar CredStore serves this endpoint to every member of the crew",
			scoped.AgentIDs, agentA)
	}
	if crewWide, ok := byID["aws-crewlink"]; !ok {
		t.Error("the crew-linked credential was not delivered")
	} else if crewWide.AgentIDs != nil {
		t.Errorf("AgentIDs = %v, want nil for a crew-linked credential", crewWide.AgentIDs)
	}

	// The JSON is the actual contract — chatbridge decodes these bytes, and a
	// renamed or missing tag does not fail to compile on either side.
	blob, err := json.Marshal(scoped)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(blob, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids, ok := wire["agent_ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != agentA {
		t.Errorf("agent_ids on the wire = %v, want [%s]", wire["agent_ids"], agentA)
	}

	crewBlob, err := json.Marshal(byID["aws-crewlink"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var crewWire map[string]any
	if err := json.Unmarshal(crewBlob, &crewWire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := crewWire["agent_ids"]; present {
		t.Error("a crew-wide credential emitted agent_ids: every existing crew's config " +
			"fingerprint moves and every shared sidecar restarts once")
	}
}

// The resolved local-model endpoint is delivered a SECOND way — as a synthetic
// OPENAI_COMPAT entry under a fixed id — and that is the per-agent endpoint
// credential in #2052's title. It must inherit the scope of the ENDPOINT_URL
// credential it came from, or one member's gateway becomes the crew's.
func TestAgentScope_ProxiedEndpointInheritsItsSourceScope(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := covCfgHandler(db)

	const (
		crewID = "awe-crew"
		agentA = "awe-agent-a"
		agentB = "awe-agent-b"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'awe-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'awe-a')`, agentA, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'awe-b')`, agentB, crewID, wsID)

	seedCredentialEnc(t, db, wsID, userID, "awe-ep", "awe-ep",
		`{"baseURL":"https://a-gateway.example/v1","apiKey":"sk-a-gateway"}`)
	execOrFatal(t, db, `UPDATE credentials SET type = 'ENDPOINT_URL', provider = 'NONE' WHERE id = 'awe-ep'`)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('awe-ac', ?, 'awe-ep', 'LOCAL_ENDPOINT', 0, datetime('now'))`, agentA)

	delivered, _, err := loadDeliveredCredentials(context.Background(), db, agentA)
	if err != nil {
		t.Fatalf("loadDeliveredCredentials: %v", err)
	}
	assigned := []mcpCredEntry{}
	for _, d := range delivered {
		if d.ID != "awe-ep" {
			continue
		}
		value, derr := decryptCredential(d.EncryptedValue)
		if derr != nil {
			t.Fatalf("decrypt: %v", derr)
		}
		assigned = append(assigned, mcpCredEntry{
			ID: d.ID, EnvVar: d.EnvVar, Value: value, Type: d.Type,
			Provider: d.Provider, AgentIDs: d.GrantedAgentIDs,
		})
	}
	if len(assigned) != 1 {
		t.Fatalf("expected the ENDPOINT_URL credential in the delivery, got %d entries", len(assigned))
	}
	if strings.Join(assigned[0].AgentIDs, ",") != agentA {
		t.Fatalf("the source ENDPOINT_URL credential is not scoped to A (%v); the rest of "+
			"this test would prove nothing", assigned[0].AgentIDs)
	}

	ep := resolveLocalModelEndpoint(context.Background(), db, newTestLogger(), wsID, assigned)
	if ep.BaseURL != "https://a-gateway.example/v1" {
		t.Fatalf("resolved endpoint = %q, want A's gateway", ep.BaseURL)
	}
	if strings.Join(ep.AgentIDs, ",") != agentA {
		t.Errorf("localModelEndpoint.AgentIDs = %v, want [%s]: the per-agent override "+
			"lost its scope on the way to the synthetic credential", ep.AgentIDs, agentA)
	}

	withEndpoint, added := h.appendProxiedEndpointCredential(assigned, ep)
	if !added {
		t.Fatal("the resolved endpoint was not delivered to the CredStore at all")
	}
	for _, c := range withEndpoint {
		if c.ID != "local-model-endpoint" {
			continue
		}
		if strings.Join(c.AgentIDs, ",") != agentA {
			t.Errorf("synthetic OPENAI_COMPAT entry AgentIDs = %v, want [%s]: one member's "+
				"gateway is served to the whole crew", c.AgentIDs, agentA)
		}
		return
	}
	t.Fatal("no synthetic local-model-endpoint entry in the extended list")
}

// #2052 through DELIVERY rather than through Select: the workspace-default
// ENDPOINT_URL fallback used to pick the newest ACTIVE row in the workspace
// with no scoping at all, so an agent holding no endpoint credential of its own
// picked up one granted to exactly one peer — and appendProxiedEndpointCredential
// then delivered it as a synthetic OPENAI_COMPAT entry with NO agent ids, i.e.
// crew-wide. Scoping the store is no defence against a credential that arrives
// labelled as everybody's.
func TestAgentScope_WorkspaceEndpointFallbackSkipsAPeersGrant(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const (
		crewID = "awf-crew"
		agentA = "awf-agent-a"
		agentB = "awf-agent-b"
	)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'C', 'awf-c')`, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'awf-a')`, agentA, crewID, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'awf-b')`, agentB, crewID, wsID)

	// The workspace default, unassigned: every agent may use it.
	seedCredentialEnc(t, db, wsID, userID, "awf-ws", "awf-ws",
		`{"baseURL":"https://shared.example/v1","apiKey":"sk-shared"}`)
	execOrFatal(t, db, `UPDATE credentials SET type = 'ENDPOINT_URL', provider = 'NONE' WHERE id = 'awf-ws'`)

	tests := []struct {
		name    string
		seed    func()
		wantURL string
	}{
		{
			name:    "an unassigned row is the workspace default",
			seed:    func() {},
			wantURL: "https://shared.example/v1",
		},
		{
			name: "a newer row granted to one peer is not",
			seed: func() {
				seedCredentialEnc(t, db, wsID, userID, "awf-peer", "awf-peer",
					`{"baseURL":"https://b-only.example/v1","apiKey":"sk-b"}`)
				execOrFatal(t, db, `UPDATE credentials SET type = 'ENDPOINT_URL', provider = 'NONE', created_at = datetime('now','+1 hour') WHERE id = 'awf-peer'`)
				execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
					VALUES ('awf-ac-b', ?, 'awf-peer', 'LOCAL_ENDPOINT', 0, datetime('now'))`, agentB)
			},
			wantURL: "https://shared.example/v1",
		},
		{
			name: "nor is a newer row bound to one peer at AGENT scope",
			seed: func() {
				seedCredentialEnc(t, db, wsID, userID, "awf-bound", "awf-bound",
					`{"baseURL":"https://b-bound.example/v1","apiKey":"sk-bb"}`)
				execOrFatal(t, db, `UPDATE credentials SET type = 'ENDPOINT_URL', provider = 'NONE', created_at = datetime('now','+2 hours') WHERE id = 'awf-bound'`)
				execOrFatal(t, db, `INSERT INTO credential_bindings (id, workspace_id, credential_id, slot, scope, agent_id, created_at)
					VALUES ('awf-b1', ?, 'awf-bound', 'LOCAL_ENDPOINT', 'AGENT', ?, datetime('now'))`, wsID, agentB)
			},
			wantURL: "https://shared.example/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.seed()
			// `assigned` is empty: A holds no endpoint credential, which is what
			// sends resolveLocalModelEndpoint down the workspace-default branch.
			ep := resolveLocalModelEndpoint(context.Background(), db, newTestLogger(), wsID, nil)
			if ep.BaseURL != tt.wantURL {
				t.Errorf("resolved endpoint = %q, want %q: an agent with no endpoint of its "+
					"own was handed one granted to a peer, and it is delivered crew-wide",
					ep.BaseURL, tt.wantURL)
			}
			if ep.AgentIDs != nil {
				t.Errorf("AgentIDs = %v, want nil: the workspace default applies to every agent",
					ep.AgentIDs)
			}
		})
	}
}
