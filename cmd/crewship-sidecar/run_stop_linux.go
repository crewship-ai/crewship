//go:build linux

package main

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// stopConfirmedExit is the helper's exit status when the leader and every
// process it started (by session and by parentage) were frozen, killed and
// each confirmed exited on its pidfd. The provider reads the exec's exit code,
// which no other process in the container can forge, and only then records a
// stop-confirmed receipt for the execution (#4774). 0 still means only the
// exact process was killed, which is what older helpers report.
const stopConfirmedExit = 10

// runStopProcessCommand stops one provider-identified process. When that
// process leads its own session (a docker exec process does), it also stops
// every process in that session and every descendant of the leader, including
// descendants that moved to their own session, and exits stopConfirmedExit
// once all of them are confirmed gone.
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
	confirmed, err := stopExactTree(*pid, *start, diagnostics)
	if err != nil {
		fmt.Fprintln(diagnostics, "exact process stop unconfirmed:", err)
		return 125
	}
	if confirmed {
		return stopConfirmedExit
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

// stopBudget bounds the whole stop, so it finishes inside the provider's
// wait for the helper.
const stopBudget = 3 * time.Second

// treeMember is one process held by pidfd while the leader's tree is stopped.
type treeMember struct {
	pid   int
	fd    int
	start uint64
}

// stopExactTree authenticates the leader (pidfd, namespace, start time) and
// signals it with SIGKILL in every outcome that reports success. If the leader
// heads its own session, everything it started is frozen first: members are
// the processes in its session plus every descendant by parent PID (Claude
// Code, for one, runs each shell command in a new session). Each is pinned
// with a pidfd, re-verified and sent SIGSTOP, repeatedly, until two
// consecutive /proc scans find the same members and every thread of each one
// stopped or gone. Stopped processes cannot fork, so the set is then closed.
// Every member and the leader are killed, and each thread group's exit is
// confirmed on its pidfd. confirmed is true only then.
//
// While the leader is alive (stopped, not reaped) its PID, and therefore the
// session ID, cannot be reused by an unrelated session. A process that was
// already orphaned (reparented away from the tree) before the stop is not a
// descendant any more and is not covered; it would survive a normal exit too.
func stopExactTree(pid int, start uint64, diagnostics io.Writer) (confirmed bool, err error) {
	if pid < 2 || start == 0 {
		return false, errors.New("invalid process identity")
	}
	deadline := time.Now().Add(stopBudget)
	leader, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(leader)
	if err := verifyHelperNamespace(pid); err != nil {
		return false, err
	}
	stat, err := readProcessStat(pid)
	if err != nil {
		return false, err
	}
	if stat.start != start {
		return false, errors.New("process start time changed")
	}
	if stat.session != pid {
		// Not a session leader: only the exact process is ours to stop.
		return false, unix.PidfdSendSignal(leader, unix.SIGKILL, nil, 0)
	}
	members := map[int]treeMember{}
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
	unconfirmed := func(format string, args ...any) (bool, error) {
		killAll()
		fmt.Fprintf(diagnostics, format+"\n", args...)
		return false, nil
	}
	if err := unix.PidfdSendSignal(leader, unix.SIGSTOP, nil, 0); err != nil {
		return false, err
	}
	settledScans := 0
	previous := ""
	for settledScans < 2 {
		if time.Now().After(deadline) {
			return unconfirmed("process tree did not settle")
		}
		procs, scanErr := scanProcesses()
		if scanErr != nil {
			return unconfirmed("process scan failed: %v", scanErr)
		}
		tree := treeOf(pid, procs)
		settled := true
		for _, member := range tree {
			held, ok := members[member.pid]
			if ok && held.start != member.start {
				// The pinned process exited and its PID was reused.
				if !pidfdExited(held.fd) {
					return unconfirmed("tree member identity changed: %d", member.pid)
				}
				unix.Close(held.fd)
				delete(members, member.pid)
				ok = false
			}
			if !ok {
				fd, openErr := unix.PidfdOpen(member.pid, 0)
				if errors.Is(openErr, unix.ESRCH) {
					settled = false
					continue
				}
				if openErr != nil {
					return unconfirmed("tree member unpinned: %v", openErr)
				}
				// Re-verify after pinning: the PID may have been recycled
				// between the scan and pidfd_open; only a verified member is
				// ever signalled.
				again, statErr := readProcessStat(member.pid)
				if statErr != nil || again.start != member.start {
					unix.Close(fd)
					settled = false
					continue
				}
				held = treeMember{pid: member.pid, fd: fd, start: member.start}
				members[member.pid] = held
				settled = false
			}
			if pidfdExited(held.fd) {
				continue
			}
			if !allThreadsStopped(member.pid) {
				settled = false
				if sigErr := unix.PidfdSendSignal(held.fd, unix.SIGSTOP, nil, 0); sigErr != nil && !errors.Is(sigErr, unix.ESRCH) {
					return unconfirmed("tree member not stoppable: %v", sigErr)
				}
			}
		}
		if !pidfdExited(leader) && !allThreadsStopped(pid) {
			settled = false
		}
		key := treeKey(tree)
		if settled && (settledScans == 0 || key == previous) {
			settledScans++
		} else {
			settledScans = 0
		}
		previous = key
		if settledScans < 2 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	killAll()
	for _, m := range members {
		if !waitPidfdExit(m.fd, time.Until(deadline)) {
			fmt.Fprintln(diagnostics, "tree member exit unconfirmed:", m.pid)
			return false, nil
		}
	}
	if !waitPidfdExit(leader, time.Until(deadline)) {
		fmt.Fprintln(diagnostics, "leader exit unconfirmed")
		return false, nil
	}
	return true, nil
}

// treeOf returns the processes in leader's session and every descendant of
// leader by parent PID, excluding leader itself.
func treeOf(leader int, procs []processStat) []processStat {
	children := map[int][]processStat{}
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p)
	}
	seen := map[int]bool{leader: true}
	var out []processStat
	queue := []int{leader}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if !seen[child.pid] {
				seen[child.pid] = true
				out = append(out, child)
				queue = append(queue, child.pid)
			}
		}
	}
	for _, p := range procs {
		if p.session == leader && !seen[p.pid] {
			seen[p.pid] = true
			out = append(out, p)
			queue = append(queue, p.pid)
		}
	}
	// Descendants of session members outside the leader's own lineage.
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if !seen[child.pid] {
				seen[child.pid] = true
				out = append(out, child)
				queue = append(queue, child.pid)
			}
		}
	}
	return out
}

func treeKey(tree []processStat) string {
	ids := make([]string, 0, len(tree))
	for _, p := range tree {
		ids = append(ids, strconv.Itoa(p.pid)+":"+strconv.FormatUint(p.start, 10))
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// pidfdExited reports whether the pinned thread group has fully exited. A
// pidfd becomes readable only once every thread is gone, so a process whose
// main thread exited while others still run is not reported as exited.
func pidfdExited(fd int) bool {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err == nil && n == 1
}

// allThreadsStopped reports whether every thread of pid is stopped or dead.
// The process state letter alone describes only the main thread.
func allThreadsStopped(pid int) bool {
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return false
	}
	for _, task := range tasks {
		data, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/stat", pid, task.Name()))
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) || errors.Is(readErr, unix.ESRCH) {
				continue
			}
			return false
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 || end+2 >= len(data) {
			return false
		}
		switch data[end+2] {
		case 'T', 't', 'Z', 'X':
		default:
			return false
		}
	}
	return true
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
		if remaining < 0 {
			remaining = 0
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int(remaining.Milliseconds())+1)
		if err == unix.EINTR {
			continue
		}
		if err == nil && n == 1 {
			return true
		}
		if err != nil || remaining == 0 || time.Now().After(deadline) {
			return false
		}
	}
}

type processStat struct {
	pid     int
	ppid    int
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
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return processStat{}, errors.New("invalid parent process")
	}
	session, err := strconv.Atoi(fields[3])
	if err != nil {
		return processStat{}, errors.New("invalid process session")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return processStat{}, errors.New("invalid process start time")
	}
	return processStat{pid: pid, ppid: ppid, state: fields[0][0], session: session, start: start}, nil
}

// scanProcesses lists every process in this PID namespace. Processes that
// vanish mid-scan are skipped.
func scanProcesses() ([]processStat, error) {
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
		out = append(out, stat)
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
