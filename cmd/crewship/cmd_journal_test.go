package main

import "testing"

func TestIsPermanentSSEError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"unauthorized", "SSE handshake: status 401", true},
		{"forbidden", "SSE handshake: status 403", true},
		{"not found", "SSE handshake: status 404", true},
		{"parse url", "parse URL: bad scheme", true},

		{"server 500", "SSE handshake: status 500", false},
		{"connection reset", "read: connection reset by peer", false},
		{"timeout", "context deadline exceeded", false},
		{"clean close", "stream closed", false},
		{"nil", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.msg != "" {
				err = errString(c.msg)
			}
			if got := isPermanentSSEError(err); got != c.want {
				t.Errorf("isPermanentSSEError(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

// errString implements error with a fixed message.
type errString string

func (e errString) Error() string { return string(e) }
