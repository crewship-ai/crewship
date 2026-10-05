//go:build linux

package managedlaunch

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestManagedLaunchRejectsDuplicateDescriptorFieldsBeforeIdentity(t *testing.T) {
	// Empty RunID keeps the old parser from reaching process/filesystem effects.
	// Duplicate keys must be rejected before any later descriptor validation.
	raw := `{"path":"/opt/native/codex","path":"/usr/bin/codex","sha256":"` + strings.Repeat("0", 64) + `","format":"static_elf","image_id":"sha256:` + strings.Repeat("1", 64) + `","revision_id":"revision","lock_sha256":"` + strings.Repeat("2", 64) + `","binary":"codex","version":"1.0.0","run_id":""}`
	err := Launch(base64.RawURLEncoding.EncodeToString([]byte(raw)), []string{"--version"})
	if err == nil || err.Error() != "managed launch: duplicate descriptor field" {
		t.Fatalf("ambiguous descriptor was not rejected at parsing boundary: %v", err)
	}
}
