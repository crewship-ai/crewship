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
	"time"

	"golang.org/x/sys/unix"
)

// sessionEmptyReceipt is printed on stdout only when the whole session the
// provider-identified leader heads was frozen, killed and confirmed gone. The
// host turns it into a durable stop-confirmed record for that execution (#4774).
const sessionEmptyReceipt = "crewship-run-stop-session-empty-v1"

// runStopProcessCommand stops one provider-identified process. When that
// process leads its own session (a docker exec process does), it also stops
// every process that stayed in the session and prints sessionEmptyReceipt once
// none is left. It never reaches processes that left the session with setsid.
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
	empty, err := stopExactSession(*pid, *start, diagnostics)
	if err != nil {
		fmt.Fprintln(diagnostics, "exact process stop unconfirmed:", err)
		return 125
	}
	if empty {
		fmt.Println(sessionEmptyReceipt)
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

// sessionMember is one process held by pidfd while its session is stopped.
type sessionMember struct {
	pid int
	fd  int
}

// stopExactSession authenticates the leader (pidfd, namespace, start time) and
// signals it with SIGKILL in every outcome that reports success. If the leader
// heads its own session, the session is first frozen: members are found by
// session ID, pinned with a pidfd, re-verified and sent SIGSTOP, repeatedly,
// until one full /proc scan finds no member that is not already stopped. A
// stopped process cannot fork, so the set is then closed. Every member and the
// leader are killed and each exit is awaited on its pidfd. empty is true only
// when a final scan finds no live process left in the session.
//
// While the leader is alive (stopped, not reaped) its PID, and therefore the
// session ID, cannot be reused by an unrelated session.
func stopExactSession(pid int, start uint64, diagnostics io.Writer) (empty bool, err error) {
	if pid < 2 || start == 0 {
		return false, errors.New("invalid process identity")
	}
	leader, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(leader)
	if err := verifyHelperNamespace(pid); err != nil {
		return false, err
	}
	observed, err := processStartTicks(pid)
	if err != nil {
		return false, err
	}
	if observed != start {
		return false, errors.New("process start time changed")
	}
	stat, err := readProcessStat(pid)
	if err != nil {
		return false, err
	}
	if stat.session != pid {
		// Not a session leader: only the exact process is ours to stop.
		return false, unix.PidfdSendSignal(leader, unix.SIGKILL, nil, 0)
	}
	members := map[int]sessionMember{}
	defer func() {
		for _, m := range members {
			unix.Close(m.fd)
		}
	}()
	killAll := func() {
		for _, m := range members {
			_ = unix.PidfdSendSignal(m.fd, unix.SIGKILL, nil, 0)
		}
		_ = unix.PidfdSendSignal(leader, unix.SIGKILL, nil, 0)
	}
	if err := unix.PidfdSendSignal(leader, unix.SIGSTOP, nil, 0); err != nil {
		return false, err
	}
	frozen := false
	for round := 0; round < 64 && !frozen; round++ {
		frozen = true
		pids, scanErr := sessionProcesses(pid)
		if scanErr != nil {
			killAll()
			fmt.Fprintln(diagnostics, "session scan failed:", scanErr)
			return false, nil
		}
		for _, member := range pids {
			if member.pid == pid || member.state == 'Z' {
				continue
			}
			if member.state != 'T' && member.state != 't' {
				frozen = false
			}
			if _, held := members[member.pid]; held {
				if member.state != 'T' && member.state != 't' {
					_ = unix.PidfdSendSignal(members[member.pid].fd, unix.SIGSTOP, nil, 0)
				}
				continue
			}
			fd, openErr := unix.PidfdOpen(member.pid, 0)
			if openErr != nil {
				if errors.Is(openErr, unix.ESRCH) {
					continue // already gone
				}
				killAll()
				fmt.Fprintln(diagnostics, "session member unpinned:", openErr)
				return false, nil
			}
			// Re-read after pinning: the PID may have been recycled between the
			// scan and pidfd_open, so only a verified member is ever signalled.
			again, statErr := readProcessStat(member.pid)
			if statErr != nil || again.session != pid || again.start != member.start {
				unix.Close(fd)
				frozen = false
				continue
			}
			members[member.pid] = sessionMember{pid: member.pid, fd: fd}
			if sigErr := unix.PidfdSendSignal(fd, unix.SIGSTOP, nil, 0); sigErr != nil && !errors.Is(sigErr, unix.ESRCH) {
				killAll()
				fmt.Fprintln(diagnostics, "session member not stoppable:", sigErr)
				return false, nil
			}
			frozen = false
		}
		if !frozen {
			time.Sleep(10 * time.Millisecond)
		}
	}
	killAll()
	if !frozen {
		fmt.Fprintln(diagnostics, "session did not settle")
		return false, nil
	}
	for _, m := range members {
		if !waitPidfdExit(m.fd, 5*time.Second) {
			fmt.Fprintln(diagnostics, "session member exit unconfirmed:", m.pid)
			return false, nil
		}
	}
	if !waitPidfdExit(leader, 5*time.Second) {
		fmt.Fprintln(diagnostics, "session leader exit unconfirmed")
		return false, nil
	}
	left, err := sessionProcesses(pid)
	if err != nil {
		fmt.Fprintln(diagnostics, "final session scan failed:", err)
		return false, nil
	}
	for _, member := range left {
		if member.state != 'Z' && member.state != 'X' {
			fmt.Fprintln(diagnostics, "session still has live process:", member.pid)
			return false, nil
		}
	}
	return true, nil
}

func verifyHelperNamespace(pid int) error {
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
	return nil
}

// waitPidfdExit reports whether the pinned process exited (a pidfd becomes
// readable at exit, zombie or reaped) within the timeout.
func waitPidfdExit(fd int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(remaining.Milliseconds())+1)
		if err == unix.EINTR {
			continue
		}
		return err == nil && n == 1
	}
}

type processStat struct {
	pid     int
	state   byte
	session int
	start   uint64
}

func readProcessStat(pid int) (processStat, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processStat{}, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return processStat{}, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || len(fields[0]) != 1 {
		return processStat{}, errors.New("short process stat")
	}
	session, err := strconv.Atoi(fields[3])
	if err != nil {
		return processStat{}, errors.New("invalid process session")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return processStat{}, errors.New("invalid process start time")
	}
	return processStat{pid: pid, state: fields[0][0], session: session, start: start}, nil
}

// sessionProcesses lists every process in this PID namespace whose session ID
// is session. Processes that vanish mid-scan are skipped.
func sessionProcesses(session int) ([]processStat, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []processStat
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid < 1 {
			continue
		}
		stat, statErr := readProcessStat(pid)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) || errors.Is(statErr, unix.ESRCH) {
				continue
			}
			return nil, statErr
		}
		if stat.session == session {
			out = append(out, stat)
		}
	}
	return out, nil
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
