package devcontainer

import "testing"

func TestIsAllowedMountSource(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{"/var/run/docker.sock", false}, // F3: socket no longer allowed (container escape)
		{"/tmp", false},                 // host /tmp bind-mount removed — container has its own /tmp tmpfs
		{"/tmp/foo", false},             // exact-match allowlist: no /tmp prefix leniency
		{"tmp", true},                   // a volume literally named "tmp" is still a valid named volume
		{"/", false},
		{"/etc/shadow", false},
		{"/etc/passwd", false},
		{"/root/.ssh/id_rsa", false},
		{"my-volume", true},                     // named volume, no leading /
		{"/dev/fuse", true},                     // still-allowed FUSE source
		{"/var/lib/docker/overlay2/foo", false}, // F3: daemon storage no longer allowed
		{"", false},
	}
	for _, tt := range tests {
		got := IsAllowedMountSource(tt.source)
		if got != tt.want {
			t.Errorf("IsAllowedMountSource(%q) = %v, want %v", tt.source, got, tt.want)
		}
	}
}
