//go:build linux

package docker

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func hostProcessIdentity(pid, initPID int) (int, uint64, error) {
	if pid < 2 || initPID < 2 {
		return 0, 0, errors.New("invalid provider process identity")
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, 0, errors.New("invalid host process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return 0, 0, errors.New("short host process stat")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return 0, 0, errors.New("invalid host process start")
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, err
	}
	initStatus, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", initPID))
	if err != nil {
		return 0, 0, err
	}
	namespacePID, err := hostNamespacePID(status, initStatus, pid, initPID)
	return namespacePID, ticks, err
}

// Docker binds ExecInspect.PID to the admitted container. Its exec leader is
// born in that container's PID namespace: PID unshare/setns affect children,
// never migrate the leader itself. Equal status hierarchy depth and an init
// PID1 refuse host-PID fallback or unexpected nested namespace mappings. The
// trusted in-container pidfd helper checks the actual namespace inode from the
// same UID, where host UID1000 cannot inspect workload UID1001 namespace links.
func hostNamespacePID(targetStatus, initStatus []byte, pid, initPID int) (int, error) {
	target, err := statusNamespacePIDs(targetStatus, pid)
	if err != nil {
		return 0, err
	}
	init, err := statusNamespacePIDs(initStatus, initPID)
	if err != nil {
		return 0, err
	}
	if len(target) < 2 || len(target) != len(init) || init[len(init)-1] != 1 || target[len(target)-1] < 2 {
		return 0, errors.New("execution PID namespace hierarchy differs from admitted container init")
	}
	return target[len(target)-1], nil
}

func statusNamespacePIDs(status []byte, hostPID int) ([]int, error) {
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		fields := strings.Fields(line)[1:]
		ids := make([]int, len(fields))
		for i, field := range fields {
			n, err := strconv.Atoi(field)
			if err != nil || n < 1 {
				return nil, errors.New("invalid process namespace PID")
			}
			ids[i] = n
		}
		if len(ids) == 0 || ids[0] != hostPID {
			return nil, errors.New("process status no longer matches provider host PID")
		}
		return ids, nil
	}
	return nil, errors.New("container process namespace unavailable")
}
