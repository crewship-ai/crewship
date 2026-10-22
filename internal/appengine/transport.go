package appengine

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

const workerServiceHeader = "X-Crewship-Worker-Service"
const workerResponseLimit = 2*MaxSnapshotBytes + 4096

// WorkerClient talks only to the operator-managed private Unix listener. It
// cannot create containers, fall back to TCP, follow redirects, or send a run
// credential. The service secret is never copied into the CLI environment.
type WorkerClient struct {
	socket, tokenFile string
	client            *http.Client
}

func ValidateWorkerPaths(socket, tokenFile string) error {
	if runtime.GOOS == "windows" || socket == tokenFile {
		return ErrInvalid
	}
	for _, p := range []string{socket, tokenFile} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || len(p) > 100 {
			return ErrInvalid
		}
	}
	return nil
}
func NewWorkerClient(socket, tokenFile string) (*WorkerClient, error) {
	if err := ValidateWorkerPaths(socket, tokenFile); err != nil {
		return nil, err
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialPrivateWorker(ctx, socket)
	}}
	return &WorkerClient{socket: socket, tokenFile: tokenFile, client: &http.Client{Transport: tr, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *WorkerClient) Run(ctx context.Context, input []byte, command Command) (EngineResult, error) {
	if c == nil || c.client == nil {
		return EngineResult{}, ErrEngineUnavailable
	}
	if err := command.validate(); err != nil {
		return EngineResult{}, err
	}
	if len(input) == 0 || len(input) > MaxSnapshotBytes {
		return EngineResult{}, ErrInvalid
	}
	secret, err := documents.ReadServiceSecret(c.tokenFile)
	if err != nil {
		return EngineResult{}, ErrEngineUnavailable
	}
	// Import snapshot travels once, as the input, rather than duplicating its
	// potentially large JSON value in both fields of the service envelope.
	command.Snapshot = nil
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(WorkerRequest{Snapshot: input, Command: command}); err != nil {
		return EngineResult{}, ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://worker/v1/candidate", &body)
	if err != nil {
		return EngineResult{}, ErrInvalid
	}
	req.Header.Set(workerServiceHeader, secret)
	req.Header.Set("Content-Type", "application/json")
	setContractHeaders(req.Header)
	resp, err := c.client.Do(req)
	if err != nil {
		return EngineResult{}, ErrEngineUnavailable
	}
	defer resp.Body.Close()
	// An incompatible or undeclared worker is unavailable, whatever it answered.
	if !hostAccepts(resp.Header) {
		return EngineResult{}, ErrEngineUnavailable
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return EngineResult{}, ErrCapacity
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge || resp.StatusCode == http.StatusUnprocessableEntity {
		return EngineResult{}, ErrInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return EngineResult{}, ErrEngineUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, workerResponseLimit+1))
	if err != nil {
		return EngineResult{}, ErrEngineUnavailable
	}
	if len(raw) > workerResponseLimit {
		return EngineResult{}, ErrInvalid
	}
	var result EngineResult
	if err := DecodeStrict(raw, &result); err != nil {
		return EngineResult{}, ErrEngineUnavailable
	}
	if len(result.Base) > MaxSnapshotBytes || len(result.Result) > MaxSnapshotBytes {
		return EngineResult{}, ErrInvalid
	}
	return result, nil
}

// NewWorkerHandler admits a bounded set of offline commands. Admission belongs
// to the persistent worker, so a host restart cannot free a still-running CLI
// slot. Every runner invocation owns and removes its private temporary files.
func NewWorkerHandler(secret string, runner Runner, maxWorkers int) (http.Handler, error) {
	if len(secret) < 32 || len(secret) > 256 || strings.TrimSpace(secret) != secret || runner == nil || maxWorkers < 1 || maxWorkers > 16 {
		return nil, ErrInvalid
	}
	slots := make(chan struct{}, maxWorkers)
	var quarantined atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(workerServiceHeader)), []byte(secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// The contract is disclosed only to an authenticated host, and an
		// incompatible host is refused before any work is admitted.
		setContractHeaders(w.Header())
		if !peerCompatible(workerContract, r.Header) {
			http.Error(w, "incompatible host protocol", http.StatusPreconditionFailed)
			return
		}
		if quarantined.Load() {
			http.Error(w, "worker unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" && r.URL.RawQuery == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/candidate" || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*MaxSnapshotBytes))
		if err != nil {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		var request WorkerRequest
		if err := DecodeStrict(raw, &request); err != nil {
			http.Error(w, "invalid candidate", http.StatusBadRequest)
			return
		}
		if request.Command.Kind == "import" {
			request.Command.Snapshot = request.Snapshot
		}
		if len(request.Snapshot) == 0 || len(request.Snapshot) > MaxSnapshotBytes || request.Command.validate() != nil {
			http.Error(w, "invalid candidate", http.StatusBadRequest)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "capacity exhausted", http.StatusTooManyRequests)
			return
		}
		if quarantined.Load() {
			http.Error(w, "worker unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := runner.Run(ctx, request.Snapshot, request.Command)
		if err != nil {
			if errors.Is(err, ErrEngineUnavailable) {
				quarantined.Store(true)
			}
			status := http.StatusUnprocessableEntity
			if errors.Is(err, ErrEngineUnavailable) {
				status = http.StatusServiceUnavailable
			}
			if errors.Is(err, ErrCapacity) {
				status = http.StatusTooManyRequests
			}
			http.Error(w, "candidate rejected", status)
			return
		}
		if len(result.Base) > MaxSnapshotBytes || len(result.Result) > MaxSnapshotBytes {
			http.Error(w, "candidate rejected", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(result)
	}), nil
}

// OfflineRunner is service-only; the application uses WorkerClient instead.
type OfflineRunner struct{ Binary, Scratch string }

func (r OfflineRunner) Run(ctx context.Context, input []byte, c Command) (EngineResult, error) {
	return RunGridCraftWorker(ctx, r.Binary, r.Scratch, input, c)
}

// Health verifies the same authenticated private transport without running a CLI.
func (c *WorkerClient) Health(ctx context.Context) error {
	secret, err := documents.ReadServiceSecret(c.tokenFile)
	if err != nil {
		return ErrEngineUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://worker/v1/health", nil)
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set(workerServiceHeader, secret)
	setContractHeaders(req.Header)
	resp, err := c.client.Do(req)
	if err != nil {
		return ErrEngineUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || !hostAccepts(resp.Header) {
		return ErrEngineUnavailable
	}
	return nil
}
