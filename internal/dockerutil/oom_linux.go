package dockerutil

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// With systemd socket activation SO_PEERCRED can identify the user manager
// that opened the listener. SCM_CREDENTIALS identifies the process that
// actually wrote the HTTP response, including on an inherited listener.
func rootlessPodmanOOMScoreAdj(ctx context.Context, endpoint string) (int, error) {
	path, local := strings.CutPrefix(endpoint, "unix://")
	if !local {
		return 0, nil
	}
	// A socket owned by root is the normal rootful Docker/Podman fast path.
	st, err := os.Stat(path)
	if err != nil {
		return 0, nil
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok && stat.Uid == 0 {
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return 0, nil
	} // let create report daemon connectivity errors
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	sock := conn.(*net.UnixConn)
	raw, err := sock.SyscallConn()
	if err != nil {
		return 0, nil
	}
	var optErr error
	if err = raw.Control(func(fd uintptr) { optErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) }); err != nil || optErr != nil {
		return 0, nil
	}
	if _, err = io.WriteString(sock, "GET /libpod/info HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		return 0, nil
	}
	var first [1]byte
	oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	n, control, _, _, err := sock.ReadMsgUnix(first[:], oob)
	if err != nil || n != 1 {
		return 0, nil
	}
	reader := bufio.NewReader(io.LimitReader(io.MultiReader(bytes.NewReader(first[:]), sock), 1<<20))
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return 0, nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, nil
	}
	var info struct {
		Host struct{ Security struct{ Rootless bool } }
	}
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil || !info.Host.Security.Rootless {
		return 0, nil
	}
	messages, err := unix.ParseSocketControlMessage(oob[:control])
	if err != nil {
		return 0, err
	}
	for _, msg := range messages {
		if msg.Header.Level != unix.SOL_SOCKET || msg.Header.Type != unix.SCM_CREDENTIALS {
			continue
		}
		cred, err := unix.ParseUnixCredentials(&msg)
		if err != nil {
			return 0, err
		}
		if cred.Uid == 0 {
			return 0, nil
		}
		if cred.Pid <= 0 {
			return 0, fmt.Errorf("Podman service PID unavailable")
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/oom_score_adj", cred.Pid))
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(strings.TrimSpace(string(data)))
	}
	return 0, fmt.Errorf("Podman service credentials unavailable")
}
