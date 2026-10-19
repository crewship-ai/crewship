package journal

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPublicAuthorizationAuditPreservesCanonicalAndPublicFields(t *testing.T) {
	for _, typ := range []EntryType{EntryMemberCapabilitiesChanged, EntryPageGrantAdded, EntryPageGrantRemoved, "page.folder_acl_changed", "unrelated.event"} {
		t.Run(string(typ), func(t *testing.T) {
			e := Entry{Type: typ, Payload: map[string]any{"action": "changed", "subject_user_id": "subject", "acl_version": 7, "previous_capabilities": []string{"chat"}, "capabilities": []string{"issue.create"}, "previous_grant": map[string]any{"issuer": "old"}, "grant": map[string]any{"issuer": "new"}, "previous_acl": map[string]any{"issuer": "old"}, "acl": map[string]any{"issuer": "new"}, "previous_acl_version": 6}}
			before, _ := json.Marshal(e)
			want := make(map[string]any)
			for k, v := range e.Payload {
				want[k] = v
			}
			switch typ {
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
