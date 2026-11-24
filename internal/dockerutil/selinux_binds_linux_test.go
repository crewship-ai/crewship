package dockerutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
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
	cli, err := client.New(client.WithHost("unix://" + socket))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	_, err = CreateCrewContainer(context.Background(), cli, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "test"},
		HostConfig: &container.HostConfig{Binds: []string{"/tmp:/mnt:rw,z"}, OomScoreAdj: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "verify Podman SELinux binds") || client.IsErrConnectionFailed(err) {
		t.Fatalf("connected peer failed validation, got %v", err)
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
	if err := verifyRelabelSources(other, binds); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "local dev=") {
		t.Fatalf("missing peer path lost its lookup error or local identity: %v", err)
	}
	if err := os.WriteFile(remoteSrc, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyRelabelSources(other, binds); err == nil || !strings.Contains(err.Error(), "peer dev=") || strings.Count(err.Error(), "ino=") != 2 {
		t.Fatalf("different inodes must report both identities: %v", err)
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

func TestPodmanSELinuxMountDiagnostics(t *testing.T) {
	proc := t.TempDir()
	if err := os.Mkdir(filepath.Join(proc, "ns"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("mnt:[1234]", filepath.Join(proc, "ns/mnt")); err != nil {
		t.Fatal(err)
	}
	const info = "1 1 0:1 / / rw - btrfs private-source private-options\n" +
		"2 1 0:2 /data /var/lib/crew\\040data rw - btrfs private-source private-options\n" +
		"3 1 0:3 / /var/lib/crew rw - btrfs private-source private-options\n"
	if err := os.WriteFile(filepath.Join(proc, "mountinfo"), []byte(info), 0600); err != nil {
		t.Fatal(err)
	}
	view := relabelMountView(proc, 42, []string{"/var/lib/crew data/output:/output:rw,z"})
	fields := map[string]slog.Value{}
	for _, attr := range view.Group() {
		fields[attr.Key] = attr.Value
	}
	if fields["pid"].Int64() != 42 || fields["mnt_ns"].String() != "mnt:[1234]" ||
		fmt.Sprint(fields["mountinfo"].Any()) != `[1 1 0:1 / / 2 1 0:2 /data /var/lib/crew\040data]` {
		t.Fatalf("unexpected namespace diagnostics: %v", fields)
	}
}
