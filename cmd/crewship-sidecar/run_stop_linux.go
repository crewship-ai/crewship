//go:build linux

package main

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// runStopProcessCommand stops one provider-identified process. It does not
// establish descendant ownership or prove that the whole run has stopped.
func runStopProcessCommand(args []string, diagnostics io.Writer) int {
	fs := flag.NewFlagSet("run-stop-process", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	pid := fs.Int("pid", 0, "provider-observed PID in this container namespace")
	start := fs.Uint64("starttime", 0, "provider-observed kernel process start ticks")
	probe := fs.Bool("probe", false, "qualify protected host capture support")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 125
	}
	if *probe {
		if len(args) != 1 || *pid != 0 || *start != 0 {
			return 125
		}
		if err := probeExactProcessSupport(diagnostics); err != nil {
			fmt.Fprintln(diagnostics, "protected capture unsupported:", err)
			return 125
		}
		return 0
	}
	if *pid < 2 || *start == 0 {
		fmt.Fprintln(diagnostics, "exact process identity required")
		return 125
	}
	if err := stopExactProcess(*pid, *start); err != nil {
		fmt.Fprintln(diagnostics, "exact process stop unconfirmed:", err)
		return 125
	}
	return 0
}

func probeExactProcessSupport(output io.Writer) error {
	if os.Geteuid() != 1001 {
		return errors.New("qualification requires workload UID1001")
	}
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.PidfdSendSignal(fd, 0, nil, 0); err != nil {
		return err
	}
	if _, err := os.Stat("/proc/self/ns/pid"); err != nil {
		return err
	}
	if _, err := processStartTicks(os.Getpid()); err != nil {
		return err
	}
	binary, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer binary.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, binary); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "crewship-run-stop-process-v1 %x\n", hash.Sum(nil))
	return err
}

func stopExactProcess(pid int, start uint64) error {
	if pid < 2 || start == 0 {
		return errors.New("invalid process identity")
	}
	// Hold the kernel process reference before reading /proc. PID reuse between
	// verification and signalling cannot redirect the signal to a new process.
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// The daemon supplied a PID mapped into this container. Authenticate the
	// namespace here, where the UID1001 helper can read its UID1001 target's
	// proc metadata without granting the host controller ptrace capabilities.
	targetNamespace, err := os.Stat(fmt.Sprintf("/proc/%d/ns/pid", pid))
	if err != nil {
		return err
	}
	ownNamespace, err := os.Stat("/proc/self/ns/pid")
	if err != nil {
		return err
	}
	if !os.SameFile(targetNamespace, ownNamespace) {
		return errors.New("process is outside helper PID namespace")
	}
	observed, err := processStartTicks(pid)
	if err != nil {
		return err
	}
	if observed != start {
		return errors.New("process start time changed")
	}
	return unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
}

func processStartTicks(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// comm is parenthesized and may itself contain spaces or closing parens.
	// The final closing paren precedes stat field3 (state); starttime is field22.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return 0, errors.New("short process stat")
	}
	value, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || value == 0 {
		return 0, errors.New("invalid process start time")
	}
	return value, nil
}
