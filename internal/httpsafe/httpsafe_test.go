package httpsafe

import (
	"errors"
	"net"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"169.254.169.254", true}, // cloud metadata
		{"10.1.2.3", true},
		{"172.20.0.1", true},
		{"192.168.5.5", true},
		{"100.64.0.1", true},
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"1.1.1.1", false},
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
		{"::ffff:127.0.0.1", true}, // IPv4-mapped loopback
		{"::ffff:8.8.8.8", false},  // IPv4-mapped public
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
			if got := IsBlockedIP(ip); got != tc.want {
				t.Fatalf("IsBlockedIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw     string
		schemes []string
		wantErr bool
	}{
		{"https://example.com/path", nil, false},
		{"http://example.com/path", nil, true},
		{"http://example.com/path", []string{"http", "https"}, false},
		{"https://example.com:8443/path", nil, false},
		{"ftp://example.com/file", nil, true},
		{"", nil, true},
		{"https://", nil, true},
		{"https://user:pw@example.com/", nil, true},
		{"https://localhost/", nil, true},
		{"https://127.0.0.1/", nil, true},
		{"https://169.254.169.254/latest", nil, true},
		{"https://10.0.0.1/", nil, true},
		{"https://[::1]/", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			_, err := ValidateURL(tc.raw, tc.schemes...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateURL(%q) err=%v wantErr=%v", tc.raw, err, tc.wantErr)
			}
			if tc.wantErr && err != nil && !errors.Is(err, ErrInvalidURL) {
				t.Fatalf("expected ErrInvalidURL, got %v", err)
			}
		})
	}
}
