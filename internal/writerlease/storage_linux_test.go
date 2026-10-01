//go:build linux

package writerlease

import "testing"

func TestSupportedLinuxFilesystemRejectsSharedAndUnknown(t *testing.T) {
	for _, kind := range []uint64{0x6969, 0xFF534D42, 0xFE534D42, 0x65735546, 0x1021997, 0x00C36400, 0xDEADBEEF} {
		if supportedLinuxFilesystem(kind) {
			t.Fatalf("unsupported filesystem %#x accepted", kind)
		}
	}
	for _, kind := range []uint64{0xEF53, 0x58465342, 0x9123683E, 0x01021994, 0x794C7630, 0x2FC12FC1, 0xF2F52010} {
		if !supportedLinuxFilesystem(kind) {
			t.Fatalf("local filesystem %#x refused", kind)
		}
	}
}
