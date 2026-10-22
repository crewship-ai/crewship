//go:build linux

package api

import (
	"encoding/json"
	"testing"
)

func TestGuestRoutineDraftPublishHintRequiresLiveGrantAndScope(t *testing.T) {
	for _, state := range []string{"active", "revoked", "inactive_issuer", "narrow_scope"} {
		t.Run(state, func(t *testing.T) {
			f := newServerPublicationHTTPFixture(t)
			switch state {
			case "revoked":
				execOrFatal(t, f.db, `DELETE FROM access_grants WHERE member_id='publish-member' AND operation='publish'`)
			case "inactive_issuer":
				execOrFatal(t, f.db, `INSERT INTO users(id,email) VALUES('inactive-publisher','inactive-publisher@example.invalid')`)
				execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('inactive-publisher',?,'inactive-publisher','VIEWER')`, f.workspace)
				execOrFatal(t, f.db, `UPDATE access_grants SET created_by='inactive-publisher' WHERE member_id='publish-member' AND operation='publish'`)
			case "narrow_scope":
				execOrFatal(t, f.db, `UPDATE cli_tokens SET scopes='["agents:write"]' WHERE user_id='publish-user'`)
			}
			var hint map[string]any
			if err := json.Unmarshal(f.call(f.publisherToken, "GET", f.path, nil, 200).Body.Bytes(), &hint); err != nil {
				t.Fatal(err)
			}
			value, present := hint["can_publish"]
			if !present || value != (state == "active") {
				t.Fatalf("publication hint ignored %s: %v", state, value)
			}
		})
	}
}
