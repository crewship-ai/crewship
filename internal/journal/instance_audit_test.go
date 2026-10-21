package journal

import (
	"database/sql"
	"testing"
)

func TestInstanceAuditMACSeparatesKeyNullAndBytes(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-instance-audit-mac-key") //gitleaks:allow -- isolated test key
	f := InstanceAuditFields{ID: "id", Action: "action", EntityType: "type", CreatedAt: "now"}
	original := InstanceAuditMAC(1, "", f)
	f.Metadata = sql.NullString{Valid: true}
	if InstanceAuditMAC(1, "", f) == original {
		t.Fatal("NULL and empty TEXT collided")
	}
	f.Metadata = sql.NullString{String: string([]byte{0xff}), Valid: true}
	raw := InstanceAuditMAC(1, "", f)
	f.Metadata.String = "\ufffd"
	if InstanceAuditMAC(1, "", f) == raw {
		t.Fatal("raw bytes were normalized")
	}
	f.Metadata = sql.NullString{}
	if InstanceAuditMAC(2, "", f) == original || InstanceAuditMAC(1, "predecessor", f) == original {
		t.Fatal("sequence/link not bound")
	}
	t.Setenv("ENCRYPTION_KEY", "different-synthetic-instance-audit-key") //gitleaks:allow -- isolated test key
	if InstanceAuditMAC(1, "", f) == original {
		t.Fatal("installation key not bound")
	}
}
