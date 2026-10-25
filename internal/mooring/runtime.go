package mooring

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxRPCFrame = 256 * 1024

// PrivatePrepareProfile is an explicit inherited-channel capability. Its
// transport ceiling never changes the public participant/batch wire profile.
const PrivatePrepareProfile = "mooring.prepare.v1"
const PrivateCausalPrepareProfile = "mooring.prepare-causal.v1"

func privatePrepareProfile(profile string) bool {
	return profile == PrivatePrepareProfile || profile == PrivateCausalPrepareProfile
}

const MaxPrivatePrepareRPCFrame = 2 * 1024 * 1024

var ErrRuntimeDisabled = errors.New("mooring runtime disabled")
var ErrRPCFrame = errors.New("invalid mooring RPC frame")
var ErrRuntimeHandshake = errors.New("incompatible mooring runtime handshake")

// RuntimeVersions describes protocol, model and engine independently. Readiness
// conveys no participant authority, durable receipt, or document state.
type RuntimeVersions struct {
	Protocol string `json:"protocol_version"`
	Model    string `json:"model_version"`
	Engine   string `json:"engine_version"`
}
type RuntimeConfig struct {
	Enabled        bool
	Binary         string
	Versions       RuntimeVersions
	ControlProfile string
}

// RPC owns a private authenticated host-child channel. Every content dispatcher
// must additionally enforce scoped authority, fencing and delivery leases.
// Read and Write can run concurrently; each direction serializes its frames.
type RPC struct {
	conn            net.Conn
	readMu, writeMu sync.Mutex
	// Set only after bootstrap, before publishing Runtime to callers.
	maxFrame int
}

func (r *RPC) frameLimit() int {
	if r.maxFrame == MaxPrivatePrepareRPCFrame {
		return MaxPrivatePrepareRPCFrame
	}
	return MaxRPCFrame
}

func (r *RPC) Write(frame []byte) error {
	if len(frame) == 0 || len(frame) > r.frameLimit() || !utf8.Valid(frame) || !rpcJSONPreflight(frame) || !json.Valid(frame) {
		return ErrRPCFrame
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
	if err := writeRPCBytes(r.conn, header[:]); err != nil {
		_ = r.conn.Close()
		return err
	}
	if err := writeRPCBytes(r.conn, frame); err != nil {
		_ = r.conn.Close()
		return err
	}
	return nil
}
func writeRPCBytes(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
func (r *RPC) Read() ([]byte, error) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	var header [4]byte
	if _, err := io.ReadFull(r.conn, header[:]); err != nil {
		_ = r.conn.Close()
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > uint32(r.frameLimit()) {
		_ = r.conn.Close()
		return nil, ErrRPCFrame
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r.conn, data); err != nil {
		_ = r.conn.Close()
		return nil, err
	}
	if !utf8.Valid(data) || !rpcJSONPreflight(data) || !json.Valid(data) {
		_ = r.conn.Close()
		return nil, ErrRPCFrame
	}
	return data, nil
}
func (r *RPC) Close() error { return r.conn.Close() }

type Runtime struct {
	RPC                    *RPC
	cmd                    *exec.Cmd
	done                   chan struct{}
	mu                     sync.Mutex
	err                    error
	exchangeOnce           sync.Once
	exchangeSlot           chan struct{}
	selectedVersions       RuntimeVersions
	selectedControlProfile string
}

func (r *Runtime) Done() <-chan struct{} { return r.done }

// Err is the exit result only after Done closes.
func (r *Runtime) Err() error { r.mu.Lock(); defer r.mu.Unlock(); return r.err }
func (r *Runtime) Close() error {
	_ = r.RPC.Close()
	_ = r.cmd.Process.Kill()
	<-r.done
	return r.Err()
}

// StartRuntime starts only a packaged, operator-selected child. Only bounded,
// allowlisted static stderr diagnostics enter logs; unknown bytes are suppressed.
// Binary authenticity and release asset pinning belong to the release caller.
func StartRuntime(ctx context.Context, c RuntimeConfig) (*Runtime, error) {
	if !c.Enabled {
		return nil, ErrRuntimeDisabled
	}
	if c.Binary == "" || c.Versions.Protocol == "" || c.Versions.Model == "" || c.Versions.Engine == "" || (c.ControlProfile != "" && !privatePrepareProfile(c.ControlProfile)) {
		return nil, ErrRuntimeHandshake
	}
	return startRuntime(ctx, c, exec.CommandContext(ctx, c.Binary, "--rpc-fd", "3"))
}
func runtimeHandshake(rpc *RPC, versions RuntimeVersions, profile string) error {
	if profile != "" && !privatePrepareProfile(profile) {
		return ErrRuntimeHandshake
	}
	return runtimeHandshakeSelected(rpc, versions, profile, validateRuntimeReady)
}

// Private construction seam. The session constructor never publishes *Runtime
// or permits legacy Exchange to read its stream.
func runtimeHandshakeSelected(rpc *RPC, versions RuntimeVersions, profile string, validate func([]byte, RuntimeVersions, string) bool) error {
	if err := rpc.conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	var offered []string
	if profile != "" {
		offered = []string{profile}
	}
	hello, _ := json.Marshal(struct {
		Type            string            `json:"type"`
		RequestID       string            `json:"request_id"`
		Supported       []RuntimeVersions `json:"supported"`
		ControlProfiles []string          `json:"control_profiles,omitempty"`
	}{"hello", "host-start", []RuntimeVersions{versions}, offered})
	if err := rpc.Write(hello); err != nil {
		return err
	}
	frame, err := rpc.Read()
	if err != nil {
		return err
	}
	if !validate(frame, versions, profile) {
		return ErrRuntimeHandshake
	}
	if err := rpc.conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	if profile != "" {
		rpc.maxFrame = MaxPrivatePrepareRPCFrame
	}
	return nil
}
