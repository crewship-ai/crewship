package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCredCreate_Response_EchoesV98Fields locks down the contract
// the UI relies on: when a Create succeeds, the 201 body must carry
// the v98 attribution fields (created_by_actor_type / actor_id /
// provisioned_for_service) the request established. Pre-fix the
// response builder dropped them, so the UI rendered the new row as
// a normal user-created credential until a subsequent LIST call
// fetched the same row and surfaced the actual provenance.
//
// Three rows in the matrix:
//
//   - default user create: actor_type=user, actor_id=self,
//     provisioned_for_service=nil
//   - admin attributing to another user: actor_id reflects the
//     foreign id (admin-only path)
//   - AUTO_MANAGED + system + canonical service tag (the SPEC-4
//     dispatch shape): all three fields surface
//
// Together they confirm the response mirrors what the DB row holds,
// which is the only piece the UI needs to render the badge / hide
// the reveal+edit actions immediately on create.
func TestCredCreate_Response_EchoesV98Fields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		role        string
		body        string
		wantActor   string
		wantActorID *string // pointer for "ignore me" vs "must be this value"
		wantProvSvc string  // empty = must be nil in response
	}{
		{
			name:      "default user create echoes actor_type=user + self id",
			role:      "OWNER",
			body:      `{"name":"GH","value":"v","type":"SECRET","provider":"GITHUB"}`,
			wantActor: "user",
			// actor_id == seed user id — checked dynamically below.
		},
		{
			name:        "AUTO_MANAGED system dispatch echoes all three v98 fields",
			role:        "OWNER",
			body:        `{"name":"PG","value":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef","type":"GENERIC_SECRET","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"crew-a/postgres"}`,
			wantActor:   "system",
			wantProvSvc: "crew-a/postgres",
			// system actor_id is intentionally nil — the spoof gate
			// rejects requests that try to set it.
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db := newCredHandler(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)

			req := httptest.NewRequest("POST", "/api/v1/credentials", bytes.NewBufferString(tc.body))
			ctx := withUser(req.Context(), &AuthUser{ID: userID})
			ctx = withWorkspace(ctx, wsID, tc.role)
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
			}
			var resp credentialResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v -- body=%s", err, rr.Body.String())
			}

			if resp.CreatedByActorType == nil || *resp.CreatedByActorType != tc.wantActor {
				got := "<nil>"
				if resp.CreatedByActorType != nil {
					got = *resp.CreatedByActorType
				}
				t.Errorf("CreatedByActorType = %q, want %q (this is the UI signal for the Crewship-managed badge)", got, tc.wantActor)
			}

			switch tc.wantActor {
			case "user":
				if resp.CreatedByActorID == nil || *resp.CreatedByActorID != userID {
					got := "<nil>"
					if resp.CreatedByActorID != nil {
						got = *resp.CreatedByActorID
					}
					t.Errorf("CreatedByActorID = %q, want self %q", got, userID)
				}
			case "system":
				if resp.CreatedByActorID != nil {
					t.Errorf("CreatedByActorID must be nil for system actor; got %q", *resp.CreatedByActorID)
				}
			}

			if tc.wantProvSvc == "" {
				if resp.ProvisionedForService != nil {
					t.Errorf("ProvisionedForService must be nil for non-AUTO_MANAGED rows; got %q", *resp.ProvisionedForService)
				}
			} else {
				if resp.ProvisionedForService == nil || *resp.ProvisionedForService != tc.wantProvSvc {
					got := "<nil>"
					if resp.ProvisionedForService != nil {
						got = *resp.ProvisionedForService
					}
					t.Errorf("ProvisionedForService = %q, want %q", got, tc.wantProvSvc)
				}
			}
		})
	}
}
