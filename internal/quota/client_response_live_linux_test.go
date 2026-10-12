//go:build linux && quota_live

package quota

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveClientRejectsUnboundOrMalformedHelperResponses(t *testing.T) {
	key := Key{"crew", "database", "data", 1}
	for _, mode := range []string{"valid", "foreign namespace", "helper refusal", "wrong key", "wrong id", "wrong size", "missing mount", "malformed", "truncated", "cancel waiting"} {
		t.Run(mode, func(t *testing.T) {
			root := liveAdmissionRoot(t)
			socket := filepath.Join(root, "helper.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				conn, err := listener.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				var req Request
				if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
					done <- err
					return
				}
				if req.Key != key || req.Namespace != "installation-a" || req.Operation != "ensure" {
					done <- errors.New("client changed bound request")
					return
				}
				response := Response{Namespace: "installation-a", Descriptor: Descriptor{ID: key.id(), Key: key, Bytes: MinBytes, Mount: "/private/mount"}}
				switch mode {
				case "foreign namespace":
					response.Namespace = "installation-b"
				case "helper refusal":
					response.Error = "quota exhausted"
				case "wrong key":
					response.Descriptor.Key.Generation++
				case "wrong id":
					response.Descriptor.ID = "other"
				case "wrong size":
					response.Descriptor.Bytes++
				case "missing mount":
					response.Descriptor.Mount = ""
				case "malformed":
					_, err := io.WriteString(conn, "not-json\n")
					done <- err
					return
				case "truncated":
					_, err := io.WriteString(conn, "{\"Namespace\":")
					done <- err
					return
				case "cancel waiting":
					cancel()
					_, _ = io.Copy(io.Discard, conn)
					done <- nil
					return
				}
				done <- json.NewEncoder(conn).Encode(response)
			}()
			got, err := (Client{Socket: socket, Namespace: "installation-a"}).Ensure(ctx, key, MinBytes, Owner{})
			if mode == "valid" {
				if err != nil || got.Key != key || got.Mount != "/private/mount" {
					t.Fatalf("bound control rejected: %+v %v", got, err)
				}
			} else if err == nil || got != (Descriptor{}) {
				t.Fatalf("invalid response accepted: %+v %v", got, err)
			}
			if mode == "cancel waiting" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation hidden: %v", err)
			}
			if mode == "helper refusal" && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("helper refusal classification lost: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
