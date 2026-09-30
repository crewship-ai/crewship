//go:build linux

// crewship-quota-helper must be installed and configured by the host admin.
// It never accepts root paths, host exports or caller UID choices over its socket.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/crewship-ai/crewship/internal/quota"
)

func main() {
	namespace := flag.String("namespace", "", "immutable per-database catalog namespace (required)")
	root := flag.String("root", "/var/lib/crewship-quota", "root-owned0700 quota catalog")
	socket := flag.String("socket", "/run/crewship-quota/helper.sock", "socket in root-owned directory")
	uid := flag.Uint("server-uid", 1000, "trusted host server UID; excludes1001/1002")
	capacity := flag.Int64("capacity-bytes", 16<<30, "aggregate preallocated image capacity")
	headroom := flag.Int64("headroom-bytes", 1<<30, "host free-space floor")
	flag.Parse()
	if *namespace == "" {
		fmt.Fprintln(os.Stderr, "namespace required")
		os.Exit(1)
	}
	if *uid > 1<<32-1 {
		fmt.Fprintln(os.Stderr, "invalid server UID")
		os.Exit(1)
	}
	selfMount, mountErr := os.Readlink("/proc/self/ns/mnt")
	hostMount, hostErr := os.Readlink("/proc/1/ns/mnt")
	if mountErr != nil || hostErr != nil || selfMount != hostMount {
		fmt.Fprintln(os.Stderr, "quota helper requires the host mount namespace")
		os.Exit(1)
	}
	backend, err := quota.NewBackend(*root, *capacity, *headroom)
	if err != nil {
		fmt.Fprintln(os.Stderr, "quota catalog unavailable:", err)
		os.Exit(1)
	}
	defer backend.Close()
	if err = backend.BindNamespace(*namespace); err != nil {
		fmt.Fprintln(os.Stderr, "quota namespace denied:", err)
		os.Exit(1)
	}
	if err = backend.Recover(); err != nil {
		fmt.Fprintln(os.Stderr, "quota catalog recovery failed:", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err = quota.ServeNamespace(ctx, *socket, uint32(*uid), backend, *namespace, notifyReady); err != nil {
		fmt.Fprintln(os.Stderr, "quota helper failed:", err)
		os.Exit(1)
	}
}

func notifyReady() error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte("READY=1\nSTATUS=Recovered quota catalog and authenticated socket ready"))
	return err
}
