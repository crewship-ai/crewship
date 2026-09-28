//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const brokerAddress = "127.0.0.1:9121"
const maxBrokerFrame = 2 << 20

type brokerFrame struct {
	Kind      string
	Token     string
	Operation string
	Body      []byte
	Status    int
	Limits    map[string]int64
}

func readBrokerFrame(r io.Reader, dst *brokerFrame) error {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return err
	}
	if n == 0 || n > maxBrokerFrame {
		return ErrDenied
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return ErrDenied
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrDenied
	}
	return nil
}
func writeBrokerFrame(w io.Writer, f brokerFrame) error {
	b, e := json.Marshal(f)
	if e != nil || len(b) > maxBrokerFrame {
		return ErrDenied
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.BigEndian, uint32(len(b)))
	out.Write(b)
	_, e = io.Copy(w, &out)
	return e
}

// RunHTTPBroker is a trusted UID-1002 bootstrap entrypoint. It provides only
// operation IDs plus bounded bodies; there is no arbitrary proxy operation.
func RunHTTPBroker(ctx context.Context, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var cfg brokerFrame
	if err := readBrokerFrame(input, &cfg); err != nil {
		return err
	}
	if cfg.Kind != "configure" || len(cfg.Token) != 64 || len(cfg.Limits) == 0 || len(cfg.Limits) > 16 {
		return ErrDenied
	}
	for id, n := range cfg.Limits {
		if !identifier.MatchString(id) || n < 0 || n > 1<<20 {
			return ErrDenied
		}
	}
	listener, err := net.Listen("tcp4", brokerAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	// A single reader detects relay EOF even while no HTTP request is active.
	replies := make(chan brokerFrame)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			var f brokerFrame
			if readBrokerFrame(input, &f) != nil {
				return
			}
			select {
			case replies <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	busy := make(chan struct{}, 1)
	server := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 16 << 10}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		deny := func(code int) { w.WriteHeader(code) }
		if r.Method != "POST" || r.URL.RawQuery != "" || r.URL.RawPath != "" || r.Header.Get("Upgrade") != "" {
			deny(403)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/operations/")
		limit, ok := cfg.Limits[id]
		if !ok || r.URL.Path != "/v1/operations/"+id || !identifier.MatchString(id) || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+cfg.Token)) != 1 {
			deny(403)
			return
		}
		select {
		case busy <- struct{}{}:
			defer func() { <-busy }()
		default:
			deny(429)
			return
		}
		body, e := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if e != nil || int64(len(body)) > limit {
			deny(413)
			return
		}
		if e = writeBrokerFrame(output, brokerFrame{Kind: "request", Operation: id, Token: cfg.Token, Body: body}); e != nil {
			deny(503)
			_ = listener.Close()
			return
		}
		select {
		case f := <-replies:
			if f.Kind != "response" || f.Status < 200 || f.Status > 599 || len(f.Body) > 1<<20 {
				deny(503)
				_ = listener.Close()
				return
			}
			w.WriteHeader(f.Status)
			_, _ = w.Write(f.Body)
		case <-stopped:
			deny(503)
		case <-ctx.Done():
			deny(503)
		}
	})
	if err = writeBrokerFrame(output, brokerFrame{Kind: "ready"}); err != nil {
		return err
	}
	serve := make(chan error, 1)
	go func() { serve <- server.Serve(listener) }()
	defer server.Close()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stopped:
		return io.EOF
	case err := <-serve:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
