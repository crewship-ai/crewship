package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCredCreate_ProvisionedForServiceSpoofGate pins the rule that
// stamping `provisioned_for_service` on a credential is reserved for
// the auto-managed dispatch path. Without this gate any MANAGER+
// caller could mint a credential that looks Crewship-managed in the
// UI (hides reveal / edit / delete) and impersonate service
// ownership metadata.
//
// Three rejection shapes + one success shape — table-driven so a
// future tier (e.g. T3 source-backed) just appends a row.
func TestCredCreate_ProvisionedForServiceSpoofGate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// JSON body excluding provisioned_for_service; the field
		// is appended uniformly below so every case exercises the
		// same gate.
		body string
		role string
		want int
	}{
		{
			name: "owner stamps provisioned_for_service without AUTO_MANAGED → 400",
			body: `{"name":"PG_X","value":"v","type":"SECRET","provider":"NONE","provisioned_for_service":"crew-a/postgres"}`,
			role: "OWNER",
			want: http.StatusBadRequest,
		},
		{
			name: "owner stamps AUTO_MANAGED without system actor → 400",
			body: `{"name":"PG_X","value":"v","type":"SECRET","provider":"AUTO_MANAGED","created_by_actor_type":"user","provisioned_for_service":"crew-a/postgres"}`,
			role: "OWNER",
			want: http.StatusBadRequest,
		},
		{
			name: "owner posts AUTO_MANAGED + system + provisioned_for_service → 201 (the legitimate dispatch path)",
			body: `{"name":"PG_X","value":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef","type":"GENERIC_SECRET","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"crew-a/postgres"}`,
			role: "OWNER",
			want: http.StatusCreated,
		},
		{
			name: "owner posts without provisioned_for_service (normal path) → 201",
			body: `{"name":"PG_X","value":"v","type":"SECRET","provider":"NONE"}`,
			role: "OWNER",
			want: http.StatusCreated,
		},
		// Shape-gate cases — even with the right provenance combo,
		// the value itself must be canonical <crew>/<service>.
		{
			name: "whitespace-only value treated as omitted → 201",
			body: `{"name":"PG_X","value":"v","type":"SECRET","provider":"NONE","provisioned_for_service":"   "}`,
			role: "OWNER",
			want: http.StatusCreated,
		},
		{
			name: "missing slash → 400",
			body: `{"name":"PG_X","value":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef","type":"GENERIC_SECRET","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"crewA-postgres"}`,
			role: "OWNER",
			want: http.StatusBadRequest,
		},
		{
			name: "empty service segment → 400",
			body: `{"name":"PG_X","value":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef","type":"GENERIC_SECRET","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"crewA/"}`,
			role: "OWNER",
			want: http.StatusBadRequest,
		},
		{
			name: "multi-slash junk → 400",
			body: `{"name":"PG_X","value":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef","type":"GENERIC_SECRET","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"crewA/postgres/extra"}`,
			role: "OWNER",
			want: http.StatusBadRequest,
		},
		{
			name: "uppercase segment violates DNS label → 400",
			body: `{"name":"PG_X","value":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef","type":"GENERIC_SECRET","provider":"AUTO_MANAGED","created_by_actor_type":"system","provisioned_for_service":"CrewA/postgres"}`,
			role: "OWNER",
			want: http.StatusBadRequest,
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
			if rr.Code != tc.want {
				t.Fatalf("status = %d (want %d), body: %s", rr.Code, tc.want, rr.Body.String())
			}
			if tc.want == http.StatusBadRequest && !strings.Contains(rr.Body.String(), "provisioned_for_service") {
				t.Errorf("rejection body should explain the gate; got %s", rr.Body.String())
			}
		})
	}
}
