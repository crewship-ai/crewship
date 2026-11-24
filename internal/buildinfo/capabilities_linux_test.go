package buildinfo

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Compare the syscall result to the kernel's independent text representation
// on both ordinary hosts and capability-constrained CI runners.
func TestRequireEmptyProcessCapabilities(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	raw, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		t.Fatal(err)
	}
	sets := map[string]bool{"CapInh:": true, "CapPrm:": true, "CapEff:": true, "CapBnd:": true, "CapAmb:": true}
	empty := true
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !sets[fields[0]] {
			continue
		}
		mask, err := strconv.ParseUint(fields[1], 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		empty = empty && mask == 0
		delete(sets, fields[0])
	}
	if len(sets) != 0 {
		t.Fatalf("missing kernel fields: %v", sets)
	}
	if err := RequireEmptyProcessCapabilities(); (err == nil) != empty {
		t.Fatalf("empty=%v but capability check returned %v", empty, err)
	}
}
