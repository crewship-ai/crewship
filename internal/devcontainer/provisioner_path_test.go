package devcontainer

import (
	"strings"
	"testing"
)

func TestSanitizeCapturedPath(t *testing.T) {
	realPath := "/home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin"

	// The exact shape seen on dev1: an 8-byte Docker stdcopy stdout frame header
	// (\x01 + 3 pad + 4 size bytes, the last undecodable → U+FFFD) prepended to
	// the captured PATH because the exec stream was read without demultiplexing.
	corrupt := "\x01\x00\x00\x00\x00\x00\x00�" + realPath

	got := sanitizeCapturedPath(corrupt)
	if got != realPath {
		t.Fatalf("sanitize did not recover the clean PATH:\n got %q\nwant %q", got, realPath)
	}
	if strings.ContainsRune(got, 0x00) {
		t.Fatal("sanitized PATH still contains a NUL byte — runc would reject the container")
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("sanitized PATH still contains the U+FFFD replacement rune")
	}
}

func TestSanitizeCapturedPath_CleanIsUnchanged(t *testing.T) {
	clean := "/usr/local/bin:/usr/bin:/bin"
	if got := sanitizeCapturedPath(clean); got != clean {
		t.Fatalf("clean PATH was altered: got %q, want %q", got, clean)
	}
}

func TestSanitizeCapturedPath_TrimsAndDropsControls(t *testing.T) {
	// Leading/trailing whitespace trimmed; embedded control bytes dropped.
	got := sanitizeCapturedPath("  /usr/bin\x07:/bin\t \n")
	if got != "/usr/bin:/bin" {
		t.Fatalf("got %q, want %q", got, "/usr/bin:/bin")
	}
}
