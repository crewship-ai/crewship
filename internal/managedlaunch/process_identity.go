package managedlaunch

import (
	"errors"
	"strconv"
	"strings"
)

// processStartIdentity accepts only the current process's session/group evidence.
func processStartIdentity(raw []byte, pid int) (uint64, error) {
	denied := errors.New("managed launch: invalid process identity")
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 || pid <= 0 {
		return 0, denied
	}
	fields := strings.Fields(string(raw[end+1:]))
	identity := strconv.Itoa(pid)
	if len(fields) < 20 || fields[2] != identity || fields[3] != identity {
		return 0, denied
	}
	stamp, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || stamp == 0 {
		return 0, denied
	}
	return stamp, nil
}
