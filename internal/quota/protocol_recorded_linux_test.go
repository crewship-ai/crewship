//go:build linux && quota_live

package quota

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These root-only socket tests use recorded backend commands: no mount, loop
// device, mkfs, or external helper is executed. Run only TestRecorded names.
func recordedPeer(t *testing.T, serve func(*net.UnixConn, Request)) Client {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("requires root for real SO_PEERCRED authentication")
	}
	socket := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		var request Request
		// Decode byte-by-byte to avoid consuming snapshot bytes after the JSON line.
		var line []byte
		for {
			var b [1]byte
			if _, err := io.ReadFull(conn, b[:]); err != nil {
				return
			}
			line = append(line, b[0])
			if b[0] == '\n' {
				break
			}
		}
		if err := json.Unmarshal(line, &request); err != nil {
			t.Error(err)
			return
		}
		serve(conn, request)
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return Client{Socket: socket, Namespace: "recorded-test"}
}

func TestRecordedClientBindsEveryDescriptorField(t *testing.T) {
	key := Key{"crew", "database", "data", 1}
	for _, operation := range []string{"ensure", "verify"} {
		for _, corrupt := range []string{"none", "namespace", "error", "id", "key", "bytes", "mount", "json"} {
			t.Run(operation+"/"+corrupt, func(t *testing.T) {
				c := recordedPeer(t, func(conn *net.UnixConn, req Request) {
					if req.Operation != operation || req.Key != key || req.Bytes != MinBytes || req.Namespace != "recorded-test" {
						t.Errorf("wrong request: %+v", req)
					}
					r := Response{Namespace: req.Namespace, Descriptor: Descriptor{ID: key.id(), Key: key, Bytes: MinBytes, Mount: "/private/mount"}}
					switch corrupt {
					case "namespace":
						r.Namespace = "foreign"
					case "error":
						r.Error = "denied"
					case "id":
						r.Descriptor.ID = "foreign"
					case "key":
						r.Descriptor.Key.Generation++
					case "bytes":
						r.Descriptor.Bytes++
					case "mount":
						r.Descriptor.Mount = ""
					case "json":
						_, _ = io.WriteString(conn, "not JSON\n")
						return
					}
					_ = json.NewEncoder(conn).Encode(r)
				})
				d, err := c.call(t.Context(), Request{Operation: operation, Key: key, Bytes: MinBytes})
				if corrupt == "none" {
					if err != nil || d.ID != key.id() {
						t.Fatalf("valid descriptor: %+v, %v", d, err)
					}
				} else if err == nil || d != (Descriptor{}) {
					t.Fatalf("accepted corrupt response: %+v, %v", d, err)
				}
			})
		}
	}
}

func TestRecordedSnapshotStreamIntegrity(t *testing.T) {
	key := Key{"crew", "database", "data", 1}
	for _, operation := range []string{"export", "import"} {
		for _, failure := range []string{"none", "descriptor", "terminal", "short"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				c := recordedPeer(t, func(conn *net.UnixConn, req Request) {
					if req.Operation != operation || req.Key != key {
						t.Errorf("wrong snapshot request: %+v", req)
					}
					r := Response{Namespace: req.Namespace, Descriptor: Descriptor{ID: key.id(), Key: key, Bytes: MinBytes, Mount: "/private/mount"}}
					if failure == "descriptor" {
						r.Descriptor.ID = "foreign"
					}
					if operation == "export" {
						_ = json.NewEncoder(conn).Encode(r)
						if failure == "short" {
							_, _ = io.WriteString(conn, "truncated")
							return
						}
						if _, err := io.CopyN(conn, snapshotUnitZeros{}, MinBytes); err != nil {
							return
						}
						terminal := Response{Namespace: req.Namespace}
						if failure == "terminal" {
							terminal.Error = "snapshot failed"
						}
						_ = json.NewEncoder(conn).Encode(terminal)
					} else {
						if _, err := io.CopyN(io.Discard, conn, MinBytes); err != nil {
							return
						}
						if failure == "terminal" {
							r.Error = "import failed"
						}
						_ = json.NewEncoder(conn).Encode(r)
					}
				})
				var err error
				if operation == "export" {
					err = c.Export(t.Context(), key, MinBytes, io.Discard)
				} else {
					var src io.Reader = snapshotUnitZeros{}
					if failure == "short" {
						src = strings.NewReader("truncated")
					}
					_, err = c.Import(t.Context(), key, MinBytes, src)
				}
				if (err == nil) != (failure == "none") {
					t.Fatalf("%s: %v", failure, err)
				}
			})
		}
	}
}

func TestRecordedServerRejectsMalformedRequestsAndDispatches(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("requires root")
	}
	u := newUnitBackend(t)
	socket := filepath.Join(t.TempDir(), "helper.sock")
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- ServeNamespace(ctx, socket, 0, u.Backend, "recorded-test", func() error { close(ready); return nil })
	}()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("serve: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("no readiness")
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	c := Client{Socket: socket, Namespace: "recorded-test"}
	key := Key{"crew", "database", "data", 1}
	if err := c.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(t.Context(), key, "reference"); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"verify", "protect", "export", "import", "unknown"} {
		_, err := c.call(t.Context(), Request{Operation: op, Key: key, Bytes: MinBytes - 1, Reference: "ref"})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s should fail closed: %v", op, err)
		}
	}
	d, err := c.Ensure(t.Context(), key, MinBytes, Owner{})
	if err != nil || d.Key != key {
		t.Fatalf("ensure: %+v %v", d, err)
	}
	for _, wire := range []string{"{\n", `{"Namespace":"foreign"}` + "\n", `{"Namespace":"recorded-test","Extra":1}` + "\n", `{"Namespace":"recorded-test"} {}` + "\n", strings.Repeat("x", 4097) + "\n"} {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.WriteString(conn, wire)
		var r Response
		err = json.NewDecoder(conn).Decode(&r)
		conn.Close()
		if err == nil {
			t.Fatalf("malformed request received response: %+v", r)
		}
	}
}
