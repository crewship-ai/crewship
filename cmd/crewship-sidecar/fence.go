package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/egressfence"
)

// Exit codes of --fence-apply / --fence-check, read by the docker provider.
// Absent also covers a table that is present but is not this fence.
const (
	fenceExitOK     = 0
	fenceExitError  = 1
	fenceExitAbsent = 3
)

func runFence(apply bool, allowUIDs string, stdout, stderr io.Writer) int {
	uids, err := parseFenceUIDs(allowUIDs)
	if err != nil {
		fmt.Fprintf(stderr, "fence: %v\n", err)
		return fenceExitError
	}
	spec := egressfence.Spec{AllowUIDs: uids}
	if apply {
		if err := egressfence.Apply(spec); err != nil {
			fmt.Fprintf(stderr, "fence: %v\n", err)
			return fenceExitError
		}
	}
	st, err := egressfence.Check(spec)
	if err != nil {
		fmt.Fprintf(stderr, "fence: %v\n", err)
		return fenceExitError
	}
	fmt.Fprintf(stdout, "fence %s\n", st)
	if !st.Present || !st.Valid {
		return fenceExitAbsent
	}
	return fenceExitOK
}

func parseFenceUIDs(s string) ([]uint32, error) {
	var out []uint32
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid uid %q", part)
		}
		out = append(out, uint32(n))
	}
	return out, nil
}
