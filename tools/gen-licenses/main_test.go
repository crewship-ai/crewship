package main

import "testing"

func TestIsLicenseFileName(t *testing.T) {
	for _, yes := range []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING", "NOTICE", "PATENTS", "LICENSE-MIT"} {
		if !isLicenseFileName(yes) {
			t.Errorf("isLicenseFileName(%q) = false, want true", yes)
		}
	}
	for _, no := range []string{"license", "README.md", "LICENSE.go", "NOTICE_test.go", "main.go", "PATENTS.txt"} {
		if isLicenseFileName(no) {
			t.Errorf("isLicenseFileName(%q) = true, want false (exact names only; anything else needs a reviewed exception)", no)
		}
	}
}
