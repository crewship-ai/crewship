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
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Request struct {
	Namespace string
	Reference string
	Operation string
	Key       Key
	Bytes     int64
	Owner     Owner
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

// helperCallTimeout caps one helper round trip; an earlier caller deadline
// or cancellation always wins.
const helperCallTimeout = 2 * time.Minute

func (c Client) call(ctx context.Context, request Request) (Descriptor, error) {
	request.Namespace = c.Namespace
	if c.Socket == "" {
		return Descriptor{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Descriptor{}, err
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, 2*time.Second)
	connection, err := (&net.Dialer{}).DialContext(dialCtx, "unix", c.Socket)
	cancelDial()
	if err != nil {
		if ctx.Err() != nil {
			return Descriptor{}, ctx.Err()
		}
		return Descriptor{}, ErrUnavailable
	}
	defer connection.Close()
	stop := armHelperDeadline(ctx, connection)
	defer stop()
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return Descriptor{}, ErrDenied
	}
	uid, err := peerUID(socket)
	if err != nil || uid != 0 {
		return Descriptor{}, ErrDenied
	}
	if err = json.NewEncoder(connection).Encode(request); err != nil {
		if ctx.Err() != nil {
			return Descriptor{}, ctx.Err()
		}
		return Descriptor{}, err
	}
	var response Response
	if err = json.NewDecoder(io.LimitReader(connection, 8192)).Decode(&response); err != nil {
		if ctx.Err() != nil {
			return Descriptor{}, ctx.Err()
		}
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
func (c Client) Ensure(ctx context.Context, k Key, n int64, owner Owner) (Descriptor, error) {
	return c.call(ctx, Request{Operation: "ensure", Key: k, Bytes: n, Owner: owner})
}
func (c Client) Verify(ctx context.Context, k Key, n int64) (Descriptor, error) {
	return c.call(ctx, Request{Operation: "verify", Key: k, Bytes: n})
}
func (c Client) Remove(ctx context.Context, k Key) error {
	_, err := c.call(ctx, Request{Operation: "remove", Key: k})
	return err
}
func (c Client) Recover(ctx context.Context) error {
	_, err := c.call(ctx, Request{Operation: "recover"})
	return err
}

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
	if os.Geteuid() != 0 || serverUID == 1001 || serverUID == 1002 || b == nil {
		return ErrDenied
	}
	if namespace != "" {
		if err := b.BindNamespace(namespace); err != nil {
			return err
		}
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
	ctx, cancelServer := context.WithCancel(ctx)
	go func() { <-ctx.Done(); listener.Close() }()
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancelServer()
	slots := make(chan struct{}, 32)
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
			return nil
		}
		workers.Add(1)
		go func(conn *net.UnixConn) {
			defer workers.Done()
			defer func() { <-slots }()
			defer conn.Close()
			stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stopClose()
			var err error
			uid, err := peerUID(conn)
			if err != nil || uid != 0 && uid != serverUID {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(helperCallTimeout))
			opCtx, cancelOp := context.WithTimeout(ctx, helperCallTimeout)
			defer cancelOp()
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
				snapshotCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
				defer cancel()
				d, readErr := b.read(request.Key)
				if readErr != nil || Validate(request.Key, request.Bytes) != nil || d.Bytes != request.Bytes {
					err = ErrDenied
					break
				}
				response.Descriptor = d
				if json.NewEncoder(conn).Encode(response) != nil {
					return
				}
				err = b.Export(snapshotCtx, request.Key, request.Bytes, conn)
				response.Descriptor = Descriptor{}
			case "import":
				_ = conn.SetDeadline(time.Now().Add(30 * time.Minute))
				snapshotCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
				defer cancel()
				response.Descriptor, err = b.Import(snapshotCtx, request.Key, request.Bytes, reader)
			case "ensure":
				response.Descriptor, err = b.Ensure(opCtx, request.Key, request.Bytes, request.Owner)
			case "verify":
				response.Descriptor, err = b.Verify(opCtx, request.Key, request.Bytes)
			case "protect":
				err = b.Protect(opCtx, request.Key, request.Reference)
			case "release":
				err = b.Release(opCtx, request.Key, request.Reference)
			case "remove":
				err = b.Remove(opCtx, request.Key)
			case "recover":
				err = b.Recover(opCtx)
			default:
				err = ErrDenied
			}
			if err != nil {
				response.Error = "operation denied or backend unavailable"
			}
			_ = json.NewEncoder(conn).Encode(response)
		}(conn)
	}
}

func (c Client) Protect(ctx context.Context, k Key, ref string) error {
	_, err := c.call(ctx, Request{Operation: "protect", Key: k, Reference: ref})
	return err
}
func (c Client) Release(ctx context.Context, k Key, ref string) error {
	_, err := c.call(ctx, Request{Operation: "release", Key: k, Reference: ref})
	return err
}

func armHelperDeadline(ctx context.Context, connection net.Conn) func() bool {
	deadline := time.Now().Add(helperCallTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = connection.SetDeadline(deadline)
	// Registered after the bounded deadline so cancellation always wins.
	return context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
}
