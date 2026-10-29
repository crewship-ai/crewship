package journal

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPublicAuthorizationAuditPreservesCanonicalAndPublicFields(t *testing.T) {
	for _, typ := range []EntryType{EntryMemberAdded, EntryMemberRemoved, EntryInvitationCreated, EntryInvitationReissued, EntryInvitationRevoked, EntryInvitationAccepted, EntryMemberCapabilitiesChanged, EntryPageGrantAdded, EntryPageGrantRemoved, "page.folder_acl_changed", "unrelated.event"} {
		t.Run(string(typ), func(t *testing.T) {
			e := Entry{Type: typ, Payload: map[string]any{"action": "changed", "subject_user_id": "subject", "acl_version": 7, "previous_capabilities": []string{"chat"}, "capabilities": []string{"issue.create"}, "previous_grant": map[string]any{"issuer": "old"}, "grant": map[string]any{"issuer": "new"}, "previous_acl": map[string]any{"issuer": "old"}, "acl": map[string]any{"issuer": "new"}, "previous_acl_version": 6, "previous_membership": map[string]any{"rights": []string{"private-right"}}, "rights": []string{"private-right"}, "access_mode": "restricted"}}
			before, _ := json.Marshal(e)
			want := make(map[string]any)
			for k, v := range e.Payload {
				want[k] = v
			}
			switch typ {
			case EntryMemberAdded, EntryMemberRemoved:
				want = map[string]any{"subject_user_id": "subject", "action": "changed"}
			case EntryInvitationCreated, EntryInvitationReissued, EntryInvitationRevoked, EntryInvitationAccepted:
				delete(want, "rights")
				delete(want, "access_mode")
			case EntryMemberCapabilitiesChanged:
				delete(want, "previous_capabilities")
				delete(want, "capabilities")
			case EntryPageGrantAdded, EntryPageGrantRemoved:
				delete(want, "previous_grant")
				delete(want, "grant")
			case "page.folder_acl_changed":
				delete(want, "previous_acl")
				delete(want, "acl")
				delete(want, "previous_acl_version")
			}
			public := PublicAuthorizationAudit(e)
			if !reflect.DeepEqual(public.Payload, want) {
				t.Fatalf("public fields differ: %v", public.Payload)
			}
			if !reflect.DeepEqual(SerializeEntry(e)["payload"], want) {
				t.Fatal("serializer differs from shared projection")
			}
			public.Payload["action"] = "changed public clone"
			after, _ := json.Marshal(e)
			if string(after) != string(before) {
				t.Fatal("canonical audit entry mutated")
			}
		})
	}
}

func TestPublicAdminSessionAuditHidesPrivateMetadata(t *testing.T) {
	for _, typ := range []EntryType{EntryAuditEntityCreated, EntryAuditEntityUpdated, EntryAuditEntityDeleted, EntryAuditEntityRestored} {
		for _, action := range []string{"user.session_revoked", "user.sessions_revoked", "user.unlocked", "unrelated.action"} {
			t.Run(string(typ)+"/"+action, func(t *testing.T) {
				entry := Entry{Type: typ, Payload: map[string]any{"action": action, "entity_id": "subject", "metadata": map[string]any{"session_id": "private-device-id", "old_failed_login_count": 7}}}
				before, _ := json.Marshal(entry)
				public := PublicAuthorizationAudit(entry)
				_, exists := public.Payload["metadata"]
				if exists != (action == "unrelated.action") {
					t.Errorf("metadata visibility for %s: %v", action, exists)
				}
				public.Payload["action"] = "changed clone"
				after, _ := json.Marshal(entry)
				if string(before) != string(after) {
					t.Fatal("canonical metadata changed")
				}
			})
		}
	}
}
