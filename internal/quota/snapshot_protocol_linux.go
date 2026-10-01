//go:build linux

package quota

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"time"
)

// snapshotConnection retains decoder lookahead, so binary bytes following the
// JSON line are never lost or confused with a second request.
func (c Client) snapshotConnection(ctx context.Context, operation string, k Key, size int64) (*net.UnixConn, *bufio.Reader, error) {
	if Validate(k, size) != nil || c.Socket == "" {
		return nil, nil, ErrDenied
	}
	raw, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		raw.Close()
		return nil, nil, ErrDenied
	}
	uid, err := peerUID(conn)
	if err != nil || uid != 0 {
		conn.Close()
		return nil, nil, ErrDenied
	}
	deadline := time.Now().Add(30 * time.Minute)
	if when, ok := ctx.Deadline(); ok && when.Before(deadline) {
		deadline = when
	}
	_ = conn.SetDeadline(deadline)
	if err = json.NewEncoder(conn).Encode(Request{Namespace: c.Namespace, Operation: operation, Key: k, Bytes: size}); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, bufio.NewReader(conn), nil
}
func snapshotResponse(reader *bufio.Reader, namespace string) (Response, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > 8192 {
		return Response{}, ErrDenied
	}
	var response Response
	if json.Unmarshal(line, &response) != nil || response.Namespace != namespace || response.Error != "" {
		return Response{}, ErrDenied
	}
	return response, nil
}
func (c Client) Export(ctx context.Context, k Key, size int64, dst io.Writer) error {
	if dst == nil {
		return ErrDenied
	}
	conn, reader, err := c.snapshotConnection(ctx, "export", k, size)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	response, err := snapshotResponse(reader, c.Namespace)
	if err != nil {
		return err
	}
	if response.Descriptor.Key != k || response.Descriptor.ID != k.id() || response.Descriptor.Bytes != size {
		return ErrDenied
	}
	if _, err = io.CopyN(dst, reader, size); err != nil {
		return err
	}
	_, err = snapshotResponse(reader, c.Namespace)
	return err
}
func (c Client) Import(ctx context.Context, k Key, size int64, src io.Reader) (Descriptor, error) {
	if src == nil {
		return Descriptor{}, ErrDenied
	}
	conn, reader, err := c.snapshotConnection(ctx, "import", k, size)
	if err != nil {
		return Descriptor{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = io.CopyN(conn, snapshotReader{ctx, src}, size); err != nil {
		return Descriptor{}, err
	}
	response, err := snapshotResponse(reader, c.Namespace)
	if err != nil {
		return Descriptor{}, err
	}
	d := response.Descriptor
	if d.Key != k || d.ID != k.id() || d.Bytes != size || d.Mount == "" {
		return Descriptor{}, ErrDenied
	}
	return d, nil
}

func (c Client) SnapshotNamespace() string { return c.Namespace }
