package admission

// Host memory headroom, read from the kernel.
//
// Two signals, and the split between them is deliberate.
//
// MemAvailable (/proc/meminfo) is the PRIMARY gate because it is the only
// PREDICTIVE one: it is the kernel's own estimate of how much a new workload
// could allocate without swapping, which is exactly the question admission
// control asks ("does one more agent container fit?"). It has been in the
// kernel since 3.14 (2014), needs no config option, and is readable from
// inside a container — where it reports the HOST's memory, which is what we
// want, since the containers we are about to start are the host's.
//
// PSI (/proc/pressure/memory) is a SECONDARY veto, never the primary gate,
// because it is LAGGING: it reports stall that has already happened. A host
// with 200 MiB free and nothing running yet reports some avg10 = 0.00 and
// would sail through a PSI-only gate. What PSI is good at is catching
// MemAvailable's known blind spot — it counts reclaimable page cache as
// available, so a host whose "available" memory is entirely hot cache looks
// roomy right up until it thrashes. Reading both costs two small file reads
// and covers both failure shapes.
//
// Neither file exists on macOS. That is not an error condition to work around
// — see the package doc in admission.go for what happens there.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrHostSignalUnavailable means this host does not publish the memory
// headroom signal at all (macOS, or a Linux without /proc mounted). Callers
// must distinguish it from "read it, and there is no room": the first says the
// gate cannot be applied, the second says the gate says no.
var ErrHostSignalUnavailable = errors.New("host memory signal unavailable on this platform")

const (
	procMeminfoPath = "/proc/meminfo"
	procPressureMem = "/proc/pressure/memory"
)

// PressureUnknown is HostMemory.SomeStallPct when the kernel does not publish
// PSI (CONFIG_PSI=n, or a kernel older than 4.20). Distinct from 0, which is
// the real reading for a host under no memory pressure at all — treating
// "unknown" as 0 would silently disable the veto on hosts that have it and
// silently pass hosts that do not.
const PressureUnknown = -1.0

// HostMemory is one reading of the host's memory headroom.
type HostMemory struct {
	// AvailableMB is MemAvailable, in MiB.
	AvailableMB int64
	// TotalMB is MemTotal, in MiB. Reported for the operator-facing message
	// only; nothing gates on a fraction of it (see admission.go).
	TotalMB int64
	// SomeStallPct is the "some avg10" line of /proc/pressure/memory as a
	// percentage, or PressureUnknown.
	SomeStallPct float64
}

// ReadHostMemory reads the live host signal.
func ReadHostMemory() (HostMemory, error) {
	return readHostMemoryFrom(procMeminfoPath, procPressureMem)
}

// readHostMemoryFrom is the testable form: paths in, so the fixtures are
// ordinary files and the "this platform has no /proc" case is exercised by
// pointing at a path that does not exist — which is literally what happens on
// macOS. No build tags: the platform difference IS the missing file.
func readHostMemoryFrom(meminfoPath, pressurePath string) (HostMemory, error) {
	raw, err := os.ReadFile(meminfoPath) // #nosec G304 -- fixed kernel paths, or test fixtures
	if err != nil {
		return HostMemory{}, fmt.Errorf("%w: %s: %v", ErrHostSignalUnavailable, meminfoPath, err)
	}
	hm, err := parseMeminfo(raw)
	if err != nil {
		return HostMemory{}, err
	}
	hm.SomeStallPct = PressureUnknown
	if praw, perr := os.ReadFile(pressurePath); perr == nil { // #nosec G304
		if v, ok := parsePressureSomeAvg10(praw); ok {
			hm.SomeStallPct = v
		}
	}
	return hm, nil
}

// parseMeminfo pulls MemAvailable and MemTotal out of /proc/meminfo.
//
// The kernel prints both in kB, which is really KiB — the field is
// `si_meminfo`'s page count scaled by PAGE_SIZE/1024 — so the MiB conversion
// is a divide by 1024, not by 1000.
func parseMeminfo(b []byte) (HostMemory, error) {
	var hm HostMemory
	var sawAvailable bool
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "MemAvailable", "MemTotal":
		default:
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && !strings.EqualFold(fields[1], "kB") {
			// Every kernel in living memory prints kB here. If one ever does
			// not, guessing the unit is how a gate silently starts comparing
			// bytes against MiB.
			return HostMemory{}, fmt.Errorf("%w: %s reported in unrecognised unit %q",
				ErrHostSignalUnavailable, key, fields[1])
		}
		if key == "MemAvailable" {
			hm.AvailableMB = kb / 1024
			sawAvailable = true
		} else {
			hm.TotalMB = kb / 1024
		}
	}
	if !sawAvailable {
		// Pre-3.14 kernels. We deliberately do NOT reconstruct it from
		// MemFree+Cached: that estimate is the reason MemAvailable was added,
		// and a gate running on a wrong number is worse than a gate that
		// stands down and says so.
		return HostMemory{}, fmt.Errorf("%w: no MemAvailable line", ErrHostSignalUnavailable)
	}
	return hm, nil
}

// parsePressureSomeAvg10 reads the 10-second "some" stall share from
// /proc/pressure/memory:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=0
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
//
// "some" (any task stalled) rather than "full" (every task stalled): by the
// time `full` moves the host is already unusable, and admission control is
// meant to act before that.
func parsePressureSomeAvg10(b []byte) (float64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[0] != "some" {
			continue
		}
		for _, f := range fields[1:] {
			k, v, ok := strings.Cut(f, "=")
			if !ok || k != "avg10" {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return 0, false
			}
			return n, true
		}
	}
	return 0, false
}
