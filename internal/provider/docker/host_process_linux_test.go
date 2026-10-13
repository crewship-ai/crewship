//go:build linux

package docker

import "testing"

func TestHostNamespacePIDRequiresAdmittedContainerHierarchy(t *testing.T) {
	for _, tc := range []struct {
		name, target, init string
		want               int
	}{
		{"container", "NSpid:\t500 23\n", "NSpid:\t400 1\n", 23},
		{"nested host", "NSpid:\t500 80 23\n", "NSpid:\t400 70 1\n", 23},
		{"host fallback", "NSpid:\t500\n", "NSpid:\t400\n", 0},
		{"deeper child namespace", "NSpid:\t500 23 2\n", "NSpid:\t400 1\n", 0},
		{"shallower namespace", "NSpid:\t500 23\n", "NSpid:\t400 70 1\n", 0},
		{"changed host PID", "NSpid:\t501 23\n", "NSpid:\t400 1\n", 0},
		{"changed init PID", "NSpid:\t500 23\n", "NSpid:\t401 1\n", 0},
		{"not init", "NSpid:\t500 23\n", "NSpid:\t400 2\n", 0},
		{"init target", "NSpid:\t500 1\n", "NSpid:\t400 1\n", 0},
		{"malformed", "NSpid:\t500 bad\n", "NSpid:\t400 1\n", 0},
		{"missing", "Name:\tsleep\n", "NSpid:\t400 1\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostNamespacePID([]byte(tc.target), []byte(tc.init), 500, 400)
			if tc.want == 0 {
				if err == nil {
					t.Fatalf("unsafe namespace accepted: %d", got)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("PID=%d err=%v", got, err)
			}
		})
	}
}
