//go:build linux

package quota

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type Request struct {
	Namespace string
	Reference string
	Operation string
	Key       Key
	Bytes     int64
}
type Response struct {
	Namespace  string
	Descriptor Descriptor
	Error      string
}
type Client struct{ Socket, Namespace string }

func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		inner = e
		if e == nil {
			uid = cred.Uid
		}
	})
	if err != nil {
		return 0, err
	}
	return uid, inner
}
func (c Client) call(request Request) (Descriptor, error) {
	request.Namespace = c.Namespace
	if c.Socket == "" {
		return Descriptor{}, ErrUnavailable
	}
	connection, err := net.DialTimeout("unix", c.Socket, 2*time.Second)
	if err != nil {
		return Descriptor{}, ErrUnavailable
	}
	defer connection.Close()
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return Descriptor{}, ErrDenied
	}
	uid, err := peerUID(socket)
	if err != nil || uid != 0 {
		return Descriptor{}, ErrDenied
	}
	_ = connection.SetDeadline(time.Now().Add(2 * time.Minute))
	if err = json.NewEncoder(connection).Encode(request); err != nil {
		return Descriptor{}, err
	}
	var response Response
	if err = json.NewDecoder(io.LimitReader(connection, 8192)).Decode(&response); err != nil {
		return Descriptor{}, err
	}
	if response.Namespace != c.Namespace {
		return Descriptor{}, ErrDenied
	}
	if response.Error != "" {
		return Descriptor{}, fmt.Errorf("quota helper: %s: %w", response.Error, ErrUnavailable)
	}
	if request.Operation == "ensure" || request.Operation == "verify" {
		d := response.Descriptor
		if d.ID != request.Key.id() || d.Key != request.Key || d.Bytes != request.Bytes || d.Mount == "" {
			return Descriptor{}, ErrDenied
		}
	}
	return response.Descriptor, nil
}
func (c Client) Ensure(k Key, n int64) (Descriptor, error) {
	return c.call(Request{Operation: "ensure", Key: k, Bytes: n})
}
func (c Client) Verify(k Key, n int64) (Descriptor, error) {
	return c.call(Request{Operation: "verify", Key: k, Bytes: n})
}
func (c Client) Remove(k Key) error {
	_, err := c.call(Request{Operation: "remove", Key: k})
	return err
}
func (c Client) Recover() error { _, err := c.call(Request{Operation: "recover"}); return err }

// Serve accepts only root and the administrator-configured host server UID.
// Agent/broker UIDs are never accepted, even if misconfigured as serverUID.
func Serve(ctx context.Context, socket string, serverUID uint32, b *Backend) error {
	return ServeWithReady(ctx, socket, serverUID, b, nil)
}

// ServeWithReady publishes readiness only after the authenticated socket exists.
func ServeWithReady(ctx context.Context, socket string, serverUID uint32, b *Backend, ready func() error) error {
	return ServeNamespace(ctx, socket, serverUID, b, "", ready)
}

// ServeNamespace binds production peers to the immutable private catalog identity.
func ServeNamespace(ctx context.Context, socket string, serverUID uint32, b *Backend, namespace string, ready func() error) error {
	if namespace != "" {
		if err := b.BindNamespace(namespace); err != nil {
			return err
		}
	}
	if os.Geteuid() != 0 || serverUID == 1001 || serverUID == 1002 || b == nil {
		return ErrDenied
	}
	if err := trustedParent(socket); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Lstat(filepath.Dir(socket), &st); err != nil || st.Uid != 0 || st.Mode&0022 != 0 {
		return ErrDenied
	}
	if info, err := os.Lstat(socket); err == nil {
		var old unix.Stat_t
		if info.Mode()&os.ModeSocket == 0 || unix.Lstat(socket, &old) != nil || old.Uid != serverUID {
			return ErrDenied
		}
		live, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
		if err == nil {
			live.Close()
			return ErrDenied
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chown(socket, int(serverUID), -1); err != nil {
		return err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	if ready != nil {
		if err = ready(); err != nil {
			return err
		}
	}
	go func() { <-ctx.Done(); listener.Close() }()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		// Serial processing plus the backend's file lock serialize disk reservations.
		func() {
			defer conn.Close()
			uid, err := peerUID(conn)
			if err != nil || uid != 0 && uid != serverUID {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
			var request Request
			reader := bufio.NewReader(conn)
			line, readErr := reader.ReadSlice('\n')
			decoder := json.NewDecoder(bytes.NewReader(line))
			decoder.DisallowUnknownFields()
			if readErr != nil || len(line) > 4096 || decoder.Decode(&request) != nil || request.Namespace != namespace {
				return
			}
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				return
			}

			response := Response{Namespace: namespace}
			switch request.Operation {
			case "export":
				_ = conn.SetDeadline(time.Now().Add(30 * time.Minute))
				d, readErr := b.read(request.Key)
				if readErr != nil || Validate(request.Key, request.Bytes) != nil || d.Bytes != request.Bytes {
					err = ErrDenied
					break
				}
				response.Descriptor = d
				if json.NewEncoder(conn).Encode(response) != nil {
					return
				}
				err = b.Export(ctx, request.Key, request.Bytes, conn)
				response.Descriptor = Descriptor{}
			case "import":
				_ = conn.SetDeadline(time.Now().Add(30 * time.Minute))
				response.Descriptor, err = b.Import(ctx, request.Key, request.Bytes, reader)
			case "ensure":
				response.Descriptor, err = b.Ensure(request.Key, request.Bytes)
			case "verify":
				response.Descriptor, err = b.Verify(request.Key, request.Bytes)
			case "protect":
				err = b.Protect(request.Key, request.Reference)
			case "release":
				err = b.Release(request.Key, request.Reference)
			case "remove":
				err = b.Remove(request.Key)
			case "recover":
				err = b.Recover()
			default:
				err = ErrDenied
			}
			if err != nil {
				response.Error = "operation denied or backend unavailable"
			}
			_ = json.NewEncoder(conn).Encode(response)
		}()
	}
}

func (c Client) Protect(k Key, ref string) error {
	_, err := c.call(Request{Operation: "protect", Key: k, Reference: ref})
	return err
}
func (c Client) Release(k Key, ref string) error {
	_, err := c.call(Request{Operation: "release", Key: k, Reference: ref})
	return err
}
