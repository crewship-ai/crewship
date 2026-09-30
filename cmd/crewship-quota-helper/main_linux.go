//go:build linux

// crewship-quota-helper must be installed and configured by the host admin.
// It never accepts root paths, host exports or caller UID choices over its socket.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/crewship-ai/crewship/internal/quota"
)

func main() {
	root := flag.String("root", "/var/lib/crewship-quota", "root-owned0700 quota catalog")
	socket := flag.String("socket", "/run/crewship-quota/helper.sock", "socket in root-owned directory")
	uid := flag.Uint("server-uid", 1000, "trusted host server UID; excludes1001/1002")
	capacity := flag.Int64("capacity-bytes", 16<<30, "aggregate preallocated image capacity")
	headroom := flag.Int64("headroom-bytes", 1<<30, "host free-space floor")
	flag.Parse()
	if *uid > 1<<32-1 {
		fmt.Fprintln(os.Stderr, "invalid server UID")
		os.Exit(1)
	}
	backend, err := quota.NewBackend(*root, *capacity, *headroom)
	if err != nil {
		fmt.Fprintln(os.Stderr, "quota catalog unavailable:", err)
		os.Exit(1)
	}
	defer backend.Close()
	if err = backend.Recover(); err != nil {
		fmt.Fprintln(os.Stderr, "quota catalog recovery failed:", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err = quota.Serve(ctx, *socket, uint32(*uid), backend); err != nil {
		fmt.Fprintln(os.Stderr, "quota helper failed:", err)
		os.Exit(1)
	}
}
