//go:build linux

package restrictedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

const leaseFile = "/broker/runtime-lease.json"
const maxLease = 15 * time.Second

// RunLeaseGuard belongs to the trusted container entrypoint, under UID 1002.
// Returning exits the init child and therefore the whole PID namespace, even
// when the host Manager is dead. An agent under UID 1001 cannot signal this
// process or replace its lease. A stopped/stalled kernel remains out of scope.
func RunLeaseGuard(ctx context.Context) error {
	return monitorLease(ctx, leaseFile, maxLease, 25*time.Millisecond)
}

// RenewLease publishes the server-issued absolute deadline atomically. The
// trusted bootstrap calls it under UID 1002; neither the agent nor a host API
// bearer can invoke this private Docker-stdin protocol.
func RenewLease(expires time.Time) error {
	return writeLease(leaseFile, expires)
}

func writeLease(path string, expires time.Time) error {
	if left := time.Until(expires); left <= 0 || left > maxLease {
		return ErrDenied
	}
	b, err := json.Marshal(expires)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".lease-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func monitorLease(ctx context.Context, path string, initial, poll time.Duration) error {
	deadline := time.Now().Add(initial)
	var previous time.Time
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			// Check the old monotonic deadline BEFORE accepting a late renewal.
			if !time.Now().Before(deadline) {
				return errors.New("restricted lease expired")
			}
			f, err := os.Open(path)
			if os.IsNotExist(err) && previous.IsZero() {
				continue // bounded initial provisioning window, no agent released
			}
			if err != nil {
				return err
			}
			var expires time.Time
			dec := json.NewDecoder(io.LimitReader(f, 4096))
			err = dec.Decode(&expires)
			_ = f.Close()
			if err != nil {
				return ErrDenied
			}
			if expires.Equal(previous) {
				continue // rereading a file must never renew its elapsed lifetime
			}
			left := time.Until(expires)
			if left <= 0 || left > maxLease {
				return ErrDenied
			}
			previous = expires
			deadline = time.Now().Add(left)
		}
	}
}
