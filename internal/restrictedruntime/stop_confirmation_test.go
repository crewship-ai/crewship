//go:build linux

package restrictedruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfirmAttemptStoppedRequiresOwnedAbsenceAndDurableTermination(t *testing.T) {
	for _, tc := range []struct {
		name, script, status string
		ok                   bool
	}{
		{"never provisioned", "exit 0", "", true},
		{"daemon unavailable", "exit 1", "", false},
		{"container still exists", "echo synthetic-container", "terminated", false},
		{"unconfirmed journal", "exit 0", "termination_unconfirmed", false},
		{"terminated and absent", "exit 0", "terminated", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "docker")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			m, err := New(filepath.Join(dir, "state"), Docker{Binary: binary}, &fixtureAuthority{}, catalogMap{}, Limits{128 << 20, 500000000, 48})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if tc.status != "" {
				if err = m.save(&Record{Attempt: "owned-attempt", Status: tc.status}); err != nil {
					t.Fatal(err)
				}
			}
			if err = m.ConfirmAttemptStopped(t.Context(), "owned-attempt"); (err == nil) != tc.ok {
				t.Fatalf("stop confirmation %v", err)
			}
		})
	}
}
