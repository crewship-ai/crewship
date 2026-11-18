//go:build windows

package runtimestage

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

func agentCLIWindowsTestRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, "crewship-agent-sdk-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	agentCLIWindowsTestACL(t, root, "")
	return root
}
func agentCLIWindowsTestACL(t *testing.T, path, extra string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + owner + "D:P(A;OICI;FA;;;" + owner + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)" + extra)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

// Exercises real NTFS files and handles, hard-link publication, readonly
// retention, ACL revalidation and a fresh generation. Structural payloads are
// not executed; this is host custody, not Docker Desktop authentication.
func TestAgentCLIWindowsRealStagingCleanAndUpgrade(t *testing.T) {
	root := agentCLIWindowsTestRoot(t)
	source := filepath.Join(root, "client")
	first := agentCLIStructuralELF(62)
	if err := os.WriteFile(source, first, 0666); err != nil {
		t.Fatal(err)
	}
	a, err := StageAgentCLI(root, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgentCLIArtifact(a); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	if original.Mode().Perm()&0222 != 0 {
		t.Fatal("published generation lost its readonly attribute")
	}
	again, err := StageAgentCLI(root, source)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := os.Stat(again.Path)
	if err != nil || !os.SameFile(original, reused) {
		t.Fatal("unchanged generation inode replaced")
	}
	next := append(agentCLIStructuralELF(183), []byte("next-generation")...)
	if err := os.WriteFile(source, next, 0666); err != nil {
		t.Fatal(err)
	}
	b, err := StageAgentCLI(root, source)
	if err != nil || b.Path == a.Path || b.Arch != "arm64" {
		t.Fatal("new generation was not published", err)
	}
	retained, err := os.ReadFile(a.Path)
	if err != nil || !bytes.Equal(retained, first) {
		t.Fatal("upgrade modified the previous generation")
	}
	if err := VerifyAgentCLIArtifact(a); err != nil {
		t.Fatal("previous generation custody changed", err)
	}
}
func TestAgentCLIWindowsRejectsCustodyChanges(t *testing.T) {
	for _, fault := range []string{"source-write-grant", "source-hardlink", "source-symlink", "ancestor-delete-child", "private-read-grant", "private-junction", "generation-write-grant"} {
		t.Run(fault, func(t *testing.T) {
			root := agentCLIWindowsTestRoot(t)
			source := filepath.Join(root, "client")
			if err := os.WriteFile(source, agentCLIStructuralELF(62), 0666); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "source-write-grant":
				agentCLIWindowsTestACL(t, source, "(A;;GW;;;WD)")
			case "source-hardlink":
				if err := os.Link(source, filepath.Join(root, "other-link")); err != nil {
					t.Fatal(err)
				}
			case "source-symlink":
				alias := filepath.Join(root, "linked-client")
				if err := os.Symlink(source, alias); err != nil {
					t.Fatal("native Windows symlink fixture could not be created", err)
				}
				source = alias
			case "ancestor-delete-child":
				agentCLIWindowsTestACL(t, root, "(A;;0x40;;;WD)")
			default:
				a, err := StageAgentCLI(root, source)
				if err != nil {
					t.Fatal(err)
				}
				if fault == "private-read-grant" {
					agentCLIWindowsTestACL(t, filepath.Dir(a.Path), "(A;;GR;;;WD)")
				} else if fault == "private-junction" {
					private := filepath.Dir(a.Path)
					target := filepath.Join(root, "retained-private")
					if err := os.Rename(private, target); err != nil {
						t.Fatal(err)
					}
					command := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/d", "/c", "mklink", "/J", private, target)
					if output, err := command.CombinedOutput(); err != nil {
						t.Fatalf("native Windows junction fixture: %v %s", err, output)
					}
				} else {
					agentCLIWindowsTestACL(t, a.Path, "(A;;GW;;;WD)")
				}
				if err := VerifyAgentCLIArtifact(a); err == nil {
					t.Fatal("accepted changed generation custody")
				}
				return
			}
			if _, err := StageAgentCLI(root, source); err == nil {
				t.Fatal("accepted untrusted Windows artifact or ancestor")
			}
		})
	}
}
