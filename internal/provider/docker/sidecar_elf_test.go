package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #953: the sidecar is bind-mounted into LINUX agent containers, so the host
// binary must be a Linux ELF even on a macOS host. The darwin release archive
// used to bundle a Mach-O sidecar, which exec'd in-container with exit 255 and
// got misdiagnosed as a glibc/musl mismatch. buildMounts must refuse a
// non-ELF sidecar with an actionable error instead.

func writeTempBinary(t *testing.T, name string, magic []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	content := append(append([]byte{}, magic...), []byte("rest-of-binary")...)
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}
	return path
}

func TestBuildMountsRejectsMachOSidecar(t *testing.T) {
	// Mach-O 64-bit little-endian magic (what a darwin/arm64 or amd64
	// goreleaser build of the sidecar starts with).
	sidecar := writeTempBinary(t, "crewship-sidecar", []byte{0xcf, 0xfa, 0xed, 0xfe})
	p := &Provider{cfg: Config{
		SidecarBinaryPath: sidecar,
		EntrypointPath:    "/host/entrypoint.sh",
	}}
	_, err := p.buildMounts("ckcrew1", "eng", "/ws", "/out", "/crew")
	if err == nil {
		t.Fatal("expected buildMounts to reject a Mach-O sidecar (#953)")
	}
	if !strings.Contains(err.Error(), "Linux ELF") {
		t.Errorf("error should explain the sidecar must be a Linux ELF, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Mach-O") {
		t.Errorf("error should name the detected Mach-O format, got: %v", err)
	}
}

func TestBuildMountsAcceptsELFSidecar(t *testing.T) {
	sidecar := writeTempBinary(t, "crewship-sidecar", []byte{0x7f, 'E', 'L', 'F'})
	p := &Provider{cfg: Config{
		SidecarBinaryPath: sidecar,
		EntrypointPath:    "/host/entrypoint.sh",
	}}
	if _, err := p.buildMounts("ckcrew1", "eng", "/ws", "/out", "/crew"); err != nil {
		t.Fatalf("buildMounts should accept an ELF sidecar: %v", err)
	}
}

func TestBuildMountsSkipsFormatCheckWhenSidecarUnreadable(t *testing.T) {
	// A nonexistent path is not this guard's job — existence failures keep
	// their existing, later error paths (Docker bind-mount failure). The
	// format guard must not turn ENOENT into a false "wrong format" error.
	p := &Provider{cfg: Config{
		SidecarBinaryPath: "/nonexistent/crewship-sidecar",
		EntrypointPath:    "/host/entrypoint.sh",
	}}
	if _, err := p.buildMounts("ckcrew1", "eng", "/ws", "/out", "/crew"); err != nil {
		t.Fatalf("buildMounts should not fail on an unreadable sidecar path: %v", err)
	}
}
