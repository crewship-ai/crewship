package journal

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSerializeEntryOwnerCreationWhitelist(t *testing.T) {
	payload := map[string]any{
		"member_id": "member", "subject_user_id": "user", "action": "owner_created", "creation_path": "signup_personal",
		"old_membership": nil, "new_membership": map[string]any{"role": "OWNER", "capabilities": []string{"credential.reveal"}},
		"role": "OWNER", "capabilities": []string{"credential.reveal"}, "email": "private@example.invalid", "setup_url": "https://example.invalid/private-setup",
	}
	before, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, action string
		kind         EntryType
		redact       bool
	}{
		{"owner_creation", "owner_created", EntryMemberOwnerCreated, true},
		{"historical_owner_creation", "owner_created", EntryMemberAccessChanged, true},
		{"other_action", "resource_access_changed", EntryMemberAccessChanged, false},
		{"other_type", "owner_created", EntryAuditEntityCreated, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := make(map[string]any, len(payload))
			for k, v := range payload {
				copy[k] = v
			}
			copy["action"] = tc.action
			entry := Entry{Type: tc.kind, Payload: copy}
			entryBefore, err := json.Marshal(entry.Payload)
			if err != nil {
				t.Fatal(err)
			}
			projected := PublicAuthorizationAudit(entry)
			actual := SerializeEntry(entry)["payload"]
			want := copy
			if tc.redact {
				want = map[string]any{"member_id": "member", "subject_user_id": "user", "action": tc.action, "creation_path": "signup_personal"}
			}
			if !reflect.DeepEqual(projected.Payload, want) {
				t.Fatal("public owner creation projection missing or unrelated event changed")
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatal("owner creation privacy whitelist missing or unrelated event changed")
			}
			entryAfter, err := json.Marshal(entry.Payload)
			if err != nil || string(entryBefore) != string(entryAfter) {
				t.Fatal("serialization changed canonical entry payload")
			}
		})
	}
	after, err := json.Marshal(payload)
	if err != nil || string(before) != string(after) {
		t.Fatal("serialization changed original raw snapshot bytes")
	}
}
