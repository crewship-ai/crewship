package journal

import "maps"

// PublicAuthorizationAudit removes private authorization snapshots from a copy
// used by public feeds and automation matching/rendering. The canonical entry
// and its keyed audit payload remain unchanged.
func PublicAuthorizationAudit(e Entry) Entry {
	e.Payload = maps.Clone(e.Payload)
	switch e.Type {
	case EntryAuditEntityCreated, EntryAuditEntityUpdated, EntryAuditEntityDeleted, EntryAuditEntityRestored:
		if action, _ := e.Payload["action"].(string); action == "user.session_revoked" || action == "user.sessions_revoked" || action == "user.unlocked" {
			delete(e.Payload, "metadata")
		}

	case EntryMemberAdded, EntryMemberRemoved:
		// Keep the producer's public whitelist across feeds, preview,
		// matching and rendering; canonical access snapshots stay intact.
		public := map[string]any{}
		for _, key := range []string{"member_id", "subject_user_id", "action"} {
			if value, ok := e.Payload[key]; ok {
				public[key] = value
			}
		}
		e.Payload = public
	case EntryMemberOwnerCreated, EntryMemberAccessChanged:
		if e.Type == EntryMemberOwnerCreated || e.Payload["action"] == "owner_created" {
			payload := map[string]any{}
			for _, key := range []string{"member_id", "subject_user_id", "action", "creation_path"} {
				if value, ok := e.Payload[key]; ok {
					payload[key] = value
				}
			}
			e.Payload = payload
		}
	case EntryInvitationCreated, EntryInvitationReissued, EntryInvitationRevoked, EntryInvitationAccepted:
		delete(e.Payload, "access_mode")
		delete(e.Payload, "rights")
	case EntryMemberCapabilitiesChanged:
		delete(e.Payload, "previous_capabilities")
		delete(e.Payload, "capabilities")
	case EntryPageGrantAdded, EntryPageGrantRemoved:
		delete(e.Payload, "previous_grant")
		delete(e.Payload, "grant")
	case EntryType("page.folder_acl_changed"):
		delete(e.Payload, "previous_acl")
		delete(e.Payload, "acl")
		delete(e.Payload, "previous_acl_version")
	}
	return e
}
