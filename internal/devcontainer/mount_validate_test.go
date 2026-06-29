package devcontainer

import "testing"

func TestIsAllowedMountSource(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{"/var/run/docker.sock", false}, // F3: socket no longer allowed (container escape)
		{"/tmp", true},
		{"/", false},
		{"/etc/shadow", false},
		{"/etc/passwd", false},
		{"/root/.ssh/id_rsa", false},
		{"my-volume", true},                     // named volume, no leading /
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
