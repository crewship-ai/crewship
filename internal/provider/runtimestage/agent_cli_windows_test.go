//go:build windows

package runtimestage

import (
	"testing"

	"golang.org/x/sys/windows"
)

// These are native Windows security descriptors, not Unix mode simulations.
// A cross-compile is not execution evidence; the Windows lane must run them.
func TestAgentCLIWindowsDescriptorCustody(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner := user.User.Sid.String()
	trusted := []*windows.SID{user.User.Sid}
	for _, fixture := range []struct {
		name, acl                string
		directory, private, want bool
	}{
		{"owner-file", "(A;;FA;;;" + owner + ")", false, false, true},
		{"public-file-read", "(A;;FA;;;" + owner + ")(A;;GR;;;WD)", false, false, true},
		{"foreign-file-write", "(A;;FA;;;" + owner + ")(A;;GW;;;WD)", false, false, false},
		{"foreign-file-delete", "(A;;FA;;;" + owner + ")(A;;SD;;;WD)", false, false, false},
		{"parent-public-read", "(A;;FA;;;" + owner + ")(A;;GRGX;;;WD)", true, false, true},
		{"parent-create-subdirectory", "(A;;FA;;;" + owner + ")(A;;0x4;;;WD)", true, false, true},
		{"parent-delete-child", "(A;;FA;;;" + owner + ")(A;;0x40;;;WD)", true, false, false},
		{"private-directory-public-read", "(A;;FA;;;" + owner + ")(A;;GR;;;WD)", true, true, false},
		{"inherit-only-not-effective", "(A;;FA;;;" + owner + ")(A;OICIIO;FA;;;WD)", true, false, true},
		{"inherited-effective-write", "(A;;FA;;;" + owner + ")(A;ID;FA;;;WD)", true, false, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString("O:" + owner + "D:P" + fixture.acl)
			if err != nil {
				t.Fatal(err)
			}
			if got := agentCLIWindowsDescriptorCustody(descriptor, trusted, fixture.directory, fixture.private); got != fixture.want {
				t.Fatalf("custody=%v, want %v", got, fixture.want)
			}
		})
	}
	for _, sddl := range []string{"O:" + owner, "O:BU" + "D:P(A;;FA;;;" + owner + ")"} {
		descriptor, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		if agentCLIWindowsDescriptorCustody(descriptor, trusted, false, false) {
			t.Fatal("accepted absent DACL or foreign owner")
		}
	}
}
