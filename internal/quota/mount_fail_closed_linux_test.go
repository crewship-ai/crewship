//go:build linux

package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMountFailureNeverAdmitsAnUnverifiedFilesystem(t *testing.T) {
	for _, failure := range []string{"unsafe mount directory", "wrong image size", "loop setup error", "unexpected device", "mount error", "unverified mount"} {
		t.Run(failure, func(t *testing.T) {
			u := newUnitBackend(t)
			k := Key{"crew", "database", "data", 1}
			u.writeEntry(t, k, MinBytes, true, true)
			d, err := u.read(k)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(d.Mount, 0700); err != nil {
				t.Fatal(err)
			}
			if failure == "unsafe mount directory" {
				if err := os.Chmod(d.Mount, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "wrong image size" {
				d.Bytes += 1 << 20
			}
			cause := errors.New("synthetic runtime failure")
			var calls [][]string
			u.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls = append(calls, append([]string{name}, args...))
				if strings.HasSuffix(name, "losetup") && len(args) > 0 && args[0] == "--find" {
					if failure == "loop setup error" {
						return nil, cause
					}
					if failure == "unexpected device" {
						return []byte("/dev/sda\n"), nil
					}
					return []byte("/dev/loop999999\n"), nil
				}
				if strings.HasSuffix(name, "/mount") && failure == "mount error" {
					return nil, cause
				}
				return nil, nil
			}
			u.attach = nil // exercise the real attach path; all system tools remain recorded fakes
			err = u.attachDescriptor(t.Context(), d)
			if err == nil {
				t.Fatal("an unverified filesystem was admitted")
			}
			if failure == "mount error" {
				want := []string{"/usr/sbin/losetup", "-d", "/dev/loop999999"}
				if !errors.Is(err, cause) || !reflect.DeepEqual(calls[len(calls)-1], want) {
					t.Fatalf("failed mount did not detach only its loop: %v %v", calls, err)
				}
			}
			if (failure == "unsafe mount directory" || failure == "wrong image size") && len(calls) != 0 {
				t.Fatalf("invalid catalog reached system tools: %v", calls)
			}
		})
	}
}

func TestUnverifiedVolumeCannotBeProtectedOrExported(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "data", 1}
	u.writeEntry(t, k, MinBytes, true, true)
	if _, err := u.Verify(t.Context(), k, MinBytes); !errors.Is(err, ErrDenied) {
		t.Fatalf("unmounted image verified: %v", err)
	}
	if _, err := u.Verify(t.Context(), k, MinBytes+(1<<20)); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong capacity verified: %v", err)
	}
	if _, err := u.Verify(t.Context(), Key{"missing", "database", "data", 1}, MinBytes); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing image verified: %v", err)
	}
	if err := u.Protect(t.Context(), k, "docker-a"); !errors.Is(err, ErrDenied) {
		t.Fatalf("unverified image protected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(u.root, "images", k.id()+".references")); !os.IsNotExist(err) {
		t.Fatalf("failed verification published a reference: %v", err)
	}
	var dst strings.Builder
	if err := u.Export(t.Context(), k, MinBytes, &dst); !errors.Is(err, ErrDenied) {
		t.Fatalf("unmounted image exported: %v", err)
	}
	if dst.Len() != 0 || len(u.commands) != 0 {
		t.Fatal("unmounted image reached destructive tools or export")
	}
	d, err := u.read(k)
	if err != nil {
		t.Fatal(err)
	}
	d.Mount = filepath.Join(t.TempDir(), "foreign")
	if err := u.verify(d); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign mount verified: %v", err)
	}
}

func TestToolEnvironmentAndFailureCause(t *testing.T) {
	t.Setenv("CREWSHIP_QUOTA_TEST_MARKER", "synthetic-secret")
	u := newUnitBackend(t)
	u.run = nil
	raw, err := u.tool(t.Context(), "/bin/sh", "-c", `printf '%s' "$PATH|$LANG|${CREWSHIP_QUOTA_TEST_MARKER-unset}"`)
	if err != nil || string(raw) != "/usr/sbin:/usr/bin:/sbin:/bin|C|unset" {
		t.Fatalf("tool inherited host environment: %q %v", raw, err)
	}
	_, err = runTool(t.Context(), "/bin/sh", "-c", `printf 'stdout'; printf 'stderr' >&2; exit 7`)
	if err == nil || !strings.Contains(err.Error(), "stdoutstderr") {
		t.Fatalf("tool failure lost diagnostics: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runTool(ctx, "/bin/sh", "-c", "exit 0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tool still ran: %v", err)
	}
}

func TestTrustedParentRejectsWritableSymlinkAndMissingAncestors(t *testing.T) {
	if err := trustedParent("/crewship-quota-test-nonexistent"); err != nil {
		t.Fatalf("root-owned parent rejected: %v", err)
	}
	parent := filepath.Join(t.TempDir(), "writable")
	if err := os.Mkdir(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := trustedParent(filepath.Join(parent, "helper.sock")); !errors.Is(err, ErrDenied) {
		t.Fatalf("writable parent admitted: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	if err := trustedParent(filepath.Join(alias, "helper.sock")); !errors.Is(err, ErrDenied) {
		t.Fatalf("symlink ancestor admitted: %v", err)
	}
	if err := trustedParent(filepath.Join(t.TempDir(), "missing", "helper.sock")); !os.IsNotExist(err) {
		t.Fatalf("missing parent admitted: %v", err)
	}
}

func TestRecoveryLogsQuarantineWithoutDestroyingReservedImage(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "orphan", 1}
	u.writeEntry(t, k, MinBytes, true, false)
	var logs []string
	u.SetLogger(func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
	if err := u.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], k.id()) || !strings.Contains(logs[0], "quarantined") {
		t.Fatalf("recovery decision not observable: %v", logs)
	}
	if used, err := u.reservedBytes(); err != nil || used != MinBytes {
		t.Fatalf("quarantined capacity lost: %d %v", used, err)
	}
}
