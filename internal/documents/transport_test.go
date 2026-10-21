//go:build !windows

package documents

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func unixTestPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "docs-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "authority.sock")
}
func TestPrivateUnixRefusesExistingPaths(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		path := unixTestPath(t)
		contents := []byte("owned by another service")
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		if stop, err := ServePrivateUnix(path, http.NotFoundHandler()); err == nil {
			stop(context.Background())
			t.Fatal("replaced existing file")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(contents) {
			t.Fatalf("existing file modified: %q %v", got, err)
		}
	})
	t.Run("live socket", func(t *testing.T) {
		path := unixTestPath(t)
		owner, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Close()
		before, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if stop, err := ServePrivateUnix(path, http.NotFoundHandler()); err == nil {
			stop(context.Background())
			t.Fatal("replaced another listener")
		}
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("existing socket changed: %v", err)
		}
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err != nil {
			t.Fatalf("owner socket no longer reachable: %v", err)
		}
		conn.Close()
	})
}
func TestPrivateUnixShutdownDrainsRequest(t *testing.T) {
	path := unixTestPath(t)
	entered := make(chan string, 1)
	release := make(chan struct{})
	stop, err := ServePrivateUnix(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if !ok {
			entered <- "missing local address"
		} else {
			entered <- addr.Network()
		}
		<-release
		io.WriteString(w, "completed")
	}))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		t.Fatalf("private socket mode: %v %v", info, err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response := make(chan error, 1)
	go func() {
		r, err := client.Get("http://private-authority/test")
		if err == nil {
			defer r.Body.Close()
			body, readErr := io.ReadAll(r.Body)
			err = readErr
			if string(body) != "completed" && err == nil {
				err = io.ErrUnexpectedEOF
			}
		}
		response <- err
	}()
	select {
	case network := <-entered:
		if network != "unix" {
			t.Fatalf("authority request arrived over %s", network)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach Unix server")
	}
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- stop(ctx)
	}()
	select {
	case err := <-stopped:
		t.Fatalf("shutdown returned while request active: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-response; err != nil {
		t.Fatalf("in-flight response was lost: %v", err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("owned socket was not removed: %v", err)
	}
}

func TestPrivateUnixShutdownPreservesReplacementPath(t *testing.T) {
	path := unixTestPath(t)
	stop, err := ServePrivateUnix(path, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	// An operator replaces the path while the original listener still exists.
	// Closing that listener must not unlink the new owner's path.
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement owned by another process")
	if err := os.WriteFile(path, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(replacement) {
		t.Fatalf("shutdown modified replacement path: %q %v", got, err)
	}
	if err := stop(ctx); err != nil {
		t.Fatalf("repeated shutdown: %v", err)
	}
}
