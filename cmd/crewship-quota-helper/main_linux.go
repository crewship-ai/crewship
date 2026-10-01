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
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	// Bad catalog entries are quarantined and reported, never fatal: one
	// broken entry must not keep the helper, and the server ordered after
	// it, down. Only an unusable catalog stops startup.
	backend.SetLogger(func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) })
	report, err := backend.RecoverReport(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "quota catalog recovery failed:", err)
		os.Exit(1)
	}
	status := report.Summary()
	fmt.Fprintln(os.Stderr, status)
	if err = quota.ServeNamespace(ctx, *socket, uint32(*uid), backend, *namespace, func() error { return notifyReady(status) }); err != nil {
		fmt.Fprintln(os.Stderr, "quota helper failed:", err)
		os.Exit(1)
	}
}

// notifyReady tells systemd the socket is live; status (the recovery
// summary, including quarantined entries) shows in `systemctl status`.
func notifyReady(status string) error {
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
	_, err = c.Write([]byte("READY=1\nSTATUS=" + strings.ReplaceAll(status, "\n", " ") + "; authenticated socket ready"))
	return err
}
