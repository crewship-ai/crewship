package sidecar

// R5 (review 2026-09-11): a transport error on the host mutation forward is NOT
// evidence that the host did not accept it.
//
// The old handler recorded any such error as a degradeReason and — unless the
// CALLER had asked for require_guaranteed — served the write from the
// in-container legacy path instead. A response dropped after the host had
// already committed therefore produced two appends of the same content, the
// second of them bypassing the ledger that is supposed to describe the file.
//
// These tests are written against a host that really commits: it appends to the
// same file the sidecar's legacy path would write, which is what the bind mount
// makes true in production (internal/api/memory_mutation.go writes the host end
// of the same inode). So "exactly once" is not an assertion about an error
// string — it is a count of the lines that actually landed in the file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// ── A host that commits ────────────────────────────────────────────────────

// ledgerHost is a stand-in for crewshipd's mutation route with the two
// properties this finding turns on: it WRITES (so a duplicate is visible in the
// bytes rather than only in a log line), and it is idempotent on operation_id
// (so a retry of an unknown outcome returns the original result instead of
// appending again).
type ledgerHost struct {
	srv *httptest.Server

	mu        sync.Mutex
	dir       string // the host end of the bind mount; set once the sidecar exists
	revision  int64
	stored    map[string]hostMutationSuccess
	commits   int // mutations actually applied — a duplicate append raises this
	mutations int // mutation requests received, including idempotent repeats

	// afterCommit runs once the write is durable and before any response is
	// sent. Returning true means it has taken over the connection and no
	// response must be written. nil means "answer normally".
	afterCommit func(t *testing.T, w http.ResponseWriter) bool
}

func newLedgerHost(t *testing.T) *ledgerHost {
	t.Helper()
	h := &ledgerHost{stored: map[string]hostMutationSuccess{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != memoryHostMutationPath {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"Not Found"}`))
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			OperationID string `json:"operation_id"`
			File        string `json:"file"`
			Op          string `json:"op"`
			Content     string `json:"content"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		h.mu.Lock()
		h.mutations++
		prior, replay := h.stored[body.OperationID]
		if !replay {
			// Commit. This is the moment after which a transport failure
			// says nothing about whether the write happened.
			h.commits++
			h.revision++
			path := filepath.Join(h.dir, body.File)
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				_, _ = f.WriteString(body.Content)
				_ = f.Close()
			}
			prior = hostMutationSuccess{
				Profile:        string(memory.ProfileGuaranteed),
				Revision:       h.revision,
				ContentSHA256:  fmt.Sprintf("sha-rev-%d", h.revision),
				LedgerRecorded: true,
				BytesWritten:   len(body.Content),
				MutationID:     "mut-" + body.OperationID,
				BaseRevision:   h.revision - 1,
			}
			h.stored[body.OperationID] = prior
		}
		after := h.afterCommit
		h.mu.Unlock()

		if after != nil && after(t, w) {
			return
		}
		out := prior
		out.Idempotent = replay
		payload, _ := json.Marshal(out)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *ledgerHost) counts() (commits, mutations int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.commits, h.mutations
}

func (h *ledgerHost) setAfterCommit(fn func(t *testing.T, w http.ResponseWriter) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.afterCommit = fn
}

// dropTheResponse hijacks the connection and closes it without answering: the
// host has committed, and the sidecar will never learn that it did.
func dropTheResponse(t *testing.T, w http.ResponseWriter) bool {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatalf("the test server does not support hijacking, so the dropped-response case cannot be built")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatalf("hijack: %v", err)
	}
	_ = conn.Close()
	return true
}

// r5Request is one append, ready to be retried verbatim — same operation id and
// all, which is the retry the unknown answer asks for.
func r5Request() MemoryWriteRequest {
	return MemoryWriteRequest{
		File:        "AGENT.md",
		Content:     "alpha-r5-line\n",
		Mode:        "append",
		OperationID: "op-r5",
		RunID:       "run-1",
		Generation:  3,
	}
}

// occurrences counts how many times marker appears in the file, or 0 when the
// file does not exist. This is the "exactly once" evidence: one line in one
// file, whoever wrote it.
func occurrences(t *testing.T, path, marker string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Count(string(raw), marker)
}

func decodeUnknown(t *testing.T, rr *httptest.ResponseRecorder) MemoryWriteUnknown {
	t.Helper()
	var out MemoryWriteUnknown
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode unknown envelope: %v (body=%s)", err, rr.Body.String())
	}
	return out
}

// postWriteCtx is postWrite with a caller-controlled context, so a test can
// make the forward time out while the host is still holding the connection.
func postWriteCtx(t *testing.T, ctx context.Context, s *Server, body MemoryWriteRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://localhost/memory/write", strings.NewReader(string(raw)))
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	s.handleMemoryWrite(rr, req)
	return rr
}

// ── Acceptance 1: committed, response dropped, retried ─────────────────────

// TestHandleMemoryWrite_DroppedResponseAfterCommitAppendsExactlyOnce is the
// finding itself. The host commits an append and the transport loses the
// answer. The sidecar must add NOTHING locally — the content is already on
// disk, and a legacy write would both duplicate the line and put bytes in the
// file that the host's revision/hash chain does not describe. The retry carries
// the same operation_id and gets the ORIGINAL result back.
func TestHandleMemoryWrite_DroppedResponseAfterCommitAppendsExactlyOnce(t *testing.T) {
	host := newLedgerHost(t)
	s, base := newHostWriteServer(t, host.srv.URL)
	host.mu.Lock()
	host.dir = base
	host.mu.Unlock()
	file := filepath.Join(base, "AGENT.md")

	// Only the first mutation loses its response.
	var once sync.Once
	host.setAfterCommit(func(t *testing.T, w http.ResponseWriter) bool {
		dropped := false
		once.Do(func() { dropped = dropTheResponse(t, w) })
		return dropped
	})

	rr := postWrite(t, s, r5Request())

	// The count comes first, deliberately. The defect is a second copy of the
	// line on disk; the 503 is only how the handler reports having refused to
	// make one. Asserting the bytes before the envelope means a regression
	// reads as "it wrote twice" rather than as "the status changed".
	commits, mutations := host.counts()
	if got := occurrences(t, file, "alpha-r5-line"); got != 1 {
		t.Fatalf("the line appears %d times after ONE append whose response was lost, want exactly 1 "+
			"(the host committed %d time(s) over %d request(s); anything more is a local write that "+
			"duplicated the append and bypassed the ledger). Response: %s",
			got, commits, mutations, rr.Body.String())
	}
	if commits != 1 || mutations != 1 {
		t.Fatalf("host commits = %d over %d mutations, want 1/1", commits, mutations)
	}

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — an unknown host outcome was resolved by writing locally: %s",
			rr.Code, rr.Body.String())
	}
	unknown := decodeUnknown(t, rr)
	if unknown.Code != "memory_mutation_unknown" {
		t.Errorf("code = %q, want memory_mutation_unknown", unknown.Code)
	}
	if !unknown.Retryable || !unknown.Sent {
		t.Errorf("the answer must say retryable and sent: %+v", unknown)
	}
	if unknown.OperationID != "op-r5" {
		t.Errorf("operation_id = %q, want op-r5 — the retry has nothing to carry without it", unknown.OperationID)
	}

	// The retry. Same operation id, byte for byte the same request.
	rr2 := postWrite(t, s, r5Request())
	if rr2.Code != http.StatusCreated {
		t.Fatalf("retry status = %d, want 201: %s", rr2.Code, rr2.Body.String())
	}
	out := decodeWrite(t, rr2)
	if !out.Idempotent {
		t.Errorf("the retry was not answered from the ledger: %+v", out)
	}
	if out.Revision != 1 || !out.RevisionChecked {
		t.Errorf("retry revision = %d checked = %v, want the ORIGINAL 1/true", out.Revision, out.RevisionChecked)
	}
	if out.MutationID != "mut-op-r5" {
		t.Errorf("mutation_id = %q, want the original mut-op-r5", out.MutationID)
	}
	if out.Degraded {
		t.Errorf("an idempotent ledger answer is not a downgrade: %+v", out)
	}

	commits, mutations = host.counts()
	if commits != 1 {
		t.Fatalf("host commits = %d after the retry, want 1 — the retry appended a second time", commits)
	}
	if mutations != 2 {
		t.Fatalf("host mutation requests = %d, want 2 (the lost one and the retry)", mutations)
	}
	if got := occurrences(t, file, "alpha-r5-line"); got != 1 {
		t.Fatalf("total effect = %d appends of the line, want exactly 1", got)
	}
}

// TestHandleMemoryWrite_TheCallerCannotWaiveTheUnknownRule. R5's wider point:
// correctness must not depend on a caller remembering to ask for it. The answer
// to a sent-but-unknown mutation is the same whether or not require_guaranteed
// was supplied, because the field is a floor and never a ceiling.
func TestHandleMemoryWrite_TheCallerCannotWaiveTheUnknownRule(t *testing.T) {
	for _, requireGuaranteed := range []bool{false, true} {
		t.Run(fmt.Sprintf("require_guaranteed=%v", requireGuaranteed), func(t *testing.T) {
			host := newLedgerHost(t)
			s, base := newHostWriteServer(t, host.srv.URL)
			host.mu.Lock()
			host.dir = base
			host.mu.Unlock()
			host.setAfterCommit(func(t *testing.T, w http.ResponseWriter) bool { return dropTheResponse(t, w) })

			req := r5Request()
			req.RequireGuaranteed = requireGuaranteed
			rr := postWrite(t, s, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
			}
			if got := decodeUnknown(t, rr).Code; got != "memory_mutation_unknown" {
				t.Errorf("code = %q, want memory_mutation_unknown regardless of the caller's preference", got)
			}
			if commits, _ := host.counts(); commits != 1 {
				t.Fatalf("host commits = %d, want 1", commits)
			}
			if got := occurrences(t, filepath.Join(base, "AGENT.md"), "alpha-r5-line"); got != 1 {
				t.Fatalf("the line appears %d times, want 1", got)
			}
		})
	}
}

// ── Acceptance 3: a timeout AFTER sending ──────────────────────────────────

// TestHandleMemoryWrite_TimeoutAfterSendingIsUnknownNotProofOfNonAcceptance.
// The host commits and then holds the connection past the caller's deadline.
// A deadline is the easiest failure to mistake for "it never got there", and it
// is the one that most often follows a commit.
func TestHandleMemoryWrite_TimeoutAfterSendingIsUnknownNotProofOfNonAcceptance(t *testing.T) {
	host := newLedgerHost(t)
	s, base := newHostWriteServer(t, host.srv.URL)
	host.mu.Lock()
	host.dir = base
	host.mu.Unlock()
	file := filepath.Join(base, "AGENT.md")

	release := make(chan struct{})
	var closeOnce sync.Once
	defer closeOnce.Do(func() { close(release) })
	host.setAfterCommit(func(t *testing.T, w http.ResponseWriter) bool {
		// Committed, and now silent. The client's deadline expires first.
		select {
		case <-release:
		case <-time.After(20 * time.Second):
		}
		return true
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	rr := postWriteCtx(t, ctx, s, r5Request())

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — a timeout after sending was treated as non-acceptance: %s",
			rr.Code, rr.Body.String())
	}
	unknown := decodeUnknown(t, rr)
	if unknown.Code != "memory_mutation_unknown" || !unknown.Sent {
		t.Errorf("a post-send timeout must be reported as sent+unknown: %+v", unknown)
	}
	if commits, _ := host.counts(); commits != 1 {
		t.Fatalf("host commits = %d, want 1", commits)
	}
	if got := occurrences(t, file, "alpha-r5-line"); got != 1 {
		t.Fatalf("the line appears %d times after a post-send timeout, want 1", got)
	}

	closeOnce.Do(func() { close(release) })
	host.setAfterCommit(nil)

	// And the retry still closes the loop, on a fresh connection.
	rr2 := postWrite(t, s, r5Request())
	if rr2.Code != http.StatusCreated {
		t.Fatalf("retry status = %d, want 201: %s", rr2.Code, rr2.Body.String())
	}
	if out := decodeWrite(t, rr2); !out.Idempotent || out.Revision != 1 {
		t.Errorf("retry did not return the original result: %+v", out)
	}
	if got := occurrences(t, file, "alpha-r5-line"); got != 1 {
		t.Fatalf("total effect = %d appends, want exactly 1", got)
	}
}

// ── Acceptance 2: connection refused BEFORE sending ────────────────────────

// TestHandleMemoryWrite_ConnectionRefusedIsProvablyNotAccepted is the one case
// the declared legacy fallback may still serve, and the test says why it is
// different: no connection was ever obtained, so no byte of the mutation
// reached any socket and the host cannot have committed it.
func TestHandleMemoryWrite_ConnectionRefusedIsProvablyNotAccepted(t *testing.T) {
	host := newLedgerHost(t)
	host.srv.Close() // nothing is listening on that port any more
	s, base := newHostWriteServer(t, host.srv.URL)
	file := filepath.Join(base, "AGENT.md")

	rr := postWrite(t, s, r5Request())
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	out := decodeWrite(t, rr)
	if out.Profile != string(memory.ProfileLegacy) || out.RevisionChecked {
		t.Errorf("a write that never left must not claim the guaranteed profile: %+v", out)
	}
	if !out.Degraded || !strings.Contains(out.DegradedReason, "never sent") {
		t.Fatalf("degraded_reason must name the send boundary, got %q", out.DegradedReason)
	}
	if commits, mutations := host.counts(); commits != 0 || mutations != 0 {
		t.Fatalf("host saw %d mutations / %d commits on a refused connection", mutations, commits)
	}
	if got := occurrences(t, file, "alpha-r5-line"); got != 1 {
		t.Fatalf("the legacy fallback wrote %d times, want exactly 1", got)
	}
}

// TestMemoryHostRequest_ClassifiesTheSendBoundary proves the classification at
// the transport itself rather than through the handler: the same call against a
// dead port and against a host that hangs up after reading the request must
// disagree about whether the request was sent.
func TestMemoryHostRequest_ClassifiesTheSendBoundary(t *testing.T) {
	t.Run("nothing listening", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := dead.URL
		dead.Close()
		s, _ := newHostWriteServer(t, url)

		_, err := s.memoryHostRequest(
			httptest.NewRequest("POST", "http://localhost/memory/write", nil),
			http.MethodPost, memoryHostMutationPath, []byte(`{}`))
		if err == nil {
			t.Fatal("want an error against a closed listener")
		}
		if memoryHostRequestReachedHost(err) {
			t.Fatalf("a refused connection was classified as sent: %v", err)
		}
	})

	t.Run("the peer hangs up after reading the request", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
		}))
		t.Cleanup(srv.Close)
		s, _ := newHostWriteServer(t, srv.URL)

		_, err := s.memoryHostRequest(
			httptest.NewRequest("POST", "http://localhost/memory/write", nil),
			http.MethodPost, memoryHostMutationPath, []byte(`{}`))
		if err == nil {
			t.Fatal("want an error when the response never arrives")
		}
		if !memoryHostRequestReachedHost(err) {
			t.Fatalf("a request the host had already read was classified as never sent: %v", err)
		}
	})

	t.Run("no IPC channel at all", func(t *testing.T) {
		s := &Server{}
		_, err := s.memoryHostRequest(
			httptest.NewRequest("POST", "http://localhost/memory/write", nil),
			http.MethodPost, memoryHostMutationPath, []byte(`{}`))
		if err == nil {
			t.Fatal("want an error with no IPC configured")
		}
		if memoryHostRequestReachedHost(err) {
			t.Fatalf("a request that was never built was classified as sent: %v", err)
		}
	})
}

// TestMemoryHostRequestReachedHost_DefaultsToSent. The default is the whole
// safety property: an error this package does not recognise must not be read as
// proof that the host did not accept the mutation.
func TestMemoryHostRequestReachedHost_DefaultsToSent(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"no error at all", nil, false},
		{"a classified pre-send failure", &memoryHostTransportError{err: errors.New("dial: refused")}, false},
		{"a classified post-send failure", &memoryHostTransportError{err: errors.New("EOF"), sent: true}, true},
		{"the same, wrapped", fmt.Errorf("call: %w", &memoryHostTransportError{err: errors.New("EOF"), sent: true}), true},
		{"a wrapped pre-send failure", fmt.Errorf("call: %w", &memoryHostTransportError{err: errors.New("refused")}), false},
		{"an unrecognised error", errors.New("something else entirely"), true},
		{"a bare net error", &net.OpError{Op: "read", Err: errors.New("reset by peer")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := memoryHostRequestReachedHost(tc.err); got != tc.want {
				t.Fatalf("memoryHostRequestReachedHost(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ── The runtime's say, not the model's ─────────────────────────────────────

// TestHandleMemoryWrite_RuntimeCanRequireTheGuaranteedProfile. R5: for the
// parallel profile the requirement has to come from server/runtime
// configuration and not from an optional field the model may omit. The caller
// here asks for nothing; the deployment does.
func TestHandleMemoryWrite_RuntimeCanRequireTheGuaranteedProfile(t *testing.T) {
	host := newLedgerHost(t)
	host.srv.Close()
	s, base := newHostWriteServer(t, host.srv.URL)
	file := filepath.Join(base, "AGENT.md")

	if memoryGuaranteedRequiredByRuntime() {
		t.Fatal("the runtime requirement is on by default; it must be opt-in")
	}
	t.Setenv("CREWSHIP_MEMORY_REQUIRE_GUARANTEED", "1")
	if !memoryGuaranteedRequiredByRuntime() {
		t.Fatal("the runtime requirement did not take effect")
	}

	req := r5Request()
	req.RequireGuaranteed = false // the model asked for nothing
	rr := postWrite(t, s, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "memory_profile_unmet") {
		t.Errorf("refusal does not carry the profile code: %s", rr.Body.String())
	}
	if got := occurrences(t, file, "alpha-r5-line"); got != 0 {
		t.Fatalf("the legacy write happened %d times despite the runtime requirement, want 0", got)
	}
}
