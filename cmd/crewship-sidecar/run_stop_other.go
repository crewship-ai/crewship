//go:build !linux

package main

import (
	"fmt"
	"io"
)

func runStopProcessCommand(_ []string, diagnostics io.Writer) int {
	fmt.Fprintln(diagnostics, "exact process stop requires Linux pidfd support")
	return 125
}
