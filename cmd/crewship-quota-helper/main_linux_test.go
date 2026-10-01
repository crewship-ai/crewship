//go:build linux

package main

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotifyReadyUsesSystemdSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", socket)
	if err = notifyReady("recovered 1 quota volume(s); 1 quarantined"); err != nil {
		t.Fatal(err)
	}
	if err = listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 256)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil || !strings.HasPrefix(string(buffer[:n]), "READY=1\n") || !strings.Contains(string(buffer[:n]), "STATUS=recovered 1 quota volume(s); 1 quarantined") {
		t.Fatalf("readiness: %q %v", buffer[:n], err)
	}
}
