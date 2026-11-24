package dockerutil

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPodmanSELinuxRejectsForwardedSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "forward.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Libpod-Api-Version", "6.2.0")
	})}
	go srv.Serve(l)
	defer srv.Close()
	if conn, err := dialPodmanBinds(context.Background(), socket, []string{"/tmp:/mnt:rw,z"}); err == nil {
		conn.Close()
		t.Fatal("a forwarding process with Podman headers authenticated as the local service")
	}
}

func TestPodmanSELinuxSourceIdentity(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "source")
	if err := os.WriteFile(src, nil, 0600); err != nil {
		t.Fatal(err)
	}
	binds := []string{src + ":/mnt:ro,z"}
	if err := verifyRelabelSources("/", binds); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "remote")
	remoteSrc := filepath.Join(other, src)
	if err := os.MkdirAll(filepath.Dir(remoteSrc), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteSrc, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyRelabelSources(other, binds); err == nil {
		t.Fatal("accepted another inode at the same absolute path")
	}
}

func TestPodmanSELinuxOrdinaryBindsAndServiceIdentity(t *testing.T) {
	for _, bind := range []string{"/host:/mnt", "/host:/mnt:ro", "/host:/mnt:rw"} {
		if sharedCrewRelabel(bind) {
			t.Fatalf("ordinary bind triggered SELinux verification: %s", bind)
		}
	}
	if !localPodmanServiceCommand([]byte("/usr/bin/podman\x00--log-level=info\x00system\x00service\x00")) {
		t.Fatal("packaged systemd service not recognized")
	}
	if localPodmanServiceCommand([]byte("/usr/bin/podman\x00system\x00dial-stdio\x00")) {
		t.Fatal("podman forwarding process accepted as the local service")
	}
}

func TestPodmanSELinuxRefusesReplaceableSource(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "staged")
	if err := os.WriteFile(source, nil, 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := verifyRelabelSources("/", []string{source + ":/mnt:ro,z"}); err == nil {
		t.Fatal("accepted a source that another user can replace")
	}
}
