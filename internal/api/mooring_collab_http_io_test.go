package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Controlled transport doubles exercise the real session handler and ingress
// lease. They do not qualify native bootstrap, network delivery or document reads.
type collaborationSessionIOWriter struct {
	*httptest.ResponseRecorder
	fence                       *collaborationIngressFence
	readDeadline, writeDeadline time.Time
	writes, flushes             int
	writeHeld, flushHeld        bool
	stalled                     bool
	body                        *collaborationSessionIOBody
	pending                     []byte
	observedRead, observedWrite time.Time
	setupHeld, unsupportedRead  bool
	stalledFlush                bool
}

func (w *collaborationSessionIOWriter) SetReadDeadline(v time.Time) error {
	if !v.IsZero() {
		w.setupHeld = w.setupHeld || sessionIOLeaseHeld(w.fence)
		w.observedRead = v
	}
	if w.unsupportedRead {
		return http.ErrNotSupported
	}
	w.readDeadline = v
	if w.body != nil {
		w.body.deadline = v
	}
	return nil
}
func (w *collaborationSessionIOWriter) SetWriteDeadline(v time.Time) error {
	if !v.IsZero() {
		w.setupHeld = w.setupHeld || sessionIOLeaseHeld(w.fence)
		w.observedWrite = v
	}
	w.writeDeadline = v
	return nil
}
func sessionIOLeaseHeld(f *collaborationIngressFence) bool {
	if f.mu.TryLock() {
		f.mu.Unlock()
		return false
	}
	return true
}
func (w *collaborationSessionIOWriter) Write(p []byte) (int, error) {
	w.writes++
	w.writeHeld = sessionIOLeaseHeld(w.fence)
	if w.stalled {
		if w.writeDeadline.IsZero() {
			return 0, os.ErrInvalid
		}
		<-time.After(time.Until(w.writeDeadline))
		return 0, os.ErrDeadlineExceeded
	}
	w.pending = append(w.pending, p...)
	return len(p), nil
}
func (w *collaborationSessionIOWriter) FlushError() error {
	w.flushes++
	w.flushHeld = sessionIOLeaseHeld(w.fence)
	if w.stalledFlush {
		if w.writeDeadline.IsZero() {
			return os.ErrInvalid
		}
		<-time.After(time.Until(w.writeDeadline))
		return os.ErrDeadlineExceeded
	}
	_, err := w.ResponseRecorder.Write(w.pending)
	w.pending = nil
	return err
}

type collaborationSessionIOBody struct {
	fence    *collaborationIngressFence
	deadline time.Time
	observed time.Time
	reads    int
	held     bool
}

func (b *collaborationSessionIOBody) Read([]byte) (int, error) {
	b.reads++
	b.observed = b.deadline
	b.held = sessionIOLeaseHeld(b.fence)
	if b.deadline.IsZero() {
		return 0, os.ErrInvalid
	}
	<-time.After(time.Until(b.deadline))
	return 0, os.ErrDeadlineExceeded
}
func (*collaborationSessionIOBody) Close() error { return nil }
func sessionIOFixture(t *testing.T) (*collaborationHTTPManager, *http.Request, *collaborationSessionIOWriter) {
	t.Helper()
	f, e := newCollaborationIngressFence()
	if e != nil {
		t.Fatal(e)
	}
	m := &collaborationHTTPManager{fence: f}
	req := httptest.NewRequest("POST", "/session", strings.NewReader(`{}`))
	w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: f}
	return m, req, w
}
func callSessionIO(m *collaborationHTTPManager, w http.ResponseWriter, r *http.Request) {
	m.fence.Wrap(http.HandlerFunc(m.session)).ServeHTTP(w, r)
}
func TestMooringSessionIOFlushesUnderOriginalLease(t *testing.T) {
	m, r, w := sessionIOFixture(t)
	callSessionIO(m, w, r)
	if w.Code != 403 {
		t.Fatalf("original no-source denial changed: %d", w.Code)
	}
	if w.writes == 0 || !w.writeHeld || w.flushes != 1 || !w.flushHeld {
		t.Fatalf("write/flush escaped lease: writes=%d held=%v flushes=%d held=%v", w.writes, w.writeHeld, w.flushes, w.flushHeld)
	}
	if w.Body.Len() == 0 || w.pending != nil || w.setupHeld || w.observedRead.IsZero() || !w.observedRead.Equal(w.observedWrite) {
		t.Fatal("buffered denial/deadline setup incomplete")
	}
	if !w.readDeadline.Equal(w.observedRead) || w.writeDeadline != (time.Time{}) {
		t.Fatal("read bound lost before server cleanup or write deadline not cleared")
	}
	if sessionIOLeaseHeld(m.fence) {
		t.Fatal("completed handler retained lease")
	}
}
func TestMooringSessionIOStalledWriteRespectsEarlierDeadline(t *testing.T) {
	m, r, w := sessionIOFixture(t)
	w.stalled = true
	end := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(r.Context(), end)
	defer cancel()
	callSessionIO(m, w, r.WithContext(ctx))
	if !w.observedRead.Equal(end) || !w.observedWrite.Equal(end) || w.setupHeld {
		t.Fatal("earlier original deadline not installed before lease")
	}
	if w.writes != 1 || !w.writeHeld || w.flushes != 1 || !w.flushHeld {
		t.Fatalf("stalled response lifecycle: writes=%d held=%v flushes=%d held=%v", w.writes, w.writeHeld, w.flushes, w.flushHeld)
	}
	if time.Now().Before(end) {
		t.Fatal("write had no effective original request deadline")
	}
	if sessionIOLeaseHeld(m.fence) {
		t.Fatal("stalled writer retained lease")
	}
}
func TestMooringSessionIOBoundsBodyBeforeRead(t *testing.T) {
	_, registry, _, ctx, _ := collaborationAdmissionFixture(t)
	m := &collaborationHTTPManager{pages: registry.pages, fence: registry.fence}
	ctx = context.WithValue(ctx, ctxWorkspaceID, registry.scope.WorkspaceID)
	end := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(ctx, end)
	defer cancel()
	r := httptest.NewRequest("POST", "/session", nil).WithContext(ctx)
	r.SetPathValue("slug", "admission-page")
	b := &collaborationSessionIOBody{fence: m.fence}
	r.Body = b
	w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: m.fence, body: b}
	callSessionIO(m, w, r)
	if b.reads != 1 || !b.held || b.observed.IsZero() || !b.observed.Equal(end) {
		t.Fatalf("unbounded/original body lifecycle: reads=%d held=%v deadline=%v", b.reads, b.held, b.observed)
	}
	if w.Code != 413 || w.flushes != 1 || !w.flushHeld {
		t.Fatalf("body error semantics/flush: status=%d flush=%d held=%v", w.Code, w.flushes, w.flushHeld)
	}
	if sessionIOLeaseHeld(m.fence) {
		t.Fatal("body timeout retained lease")
	}
}
func TestMooringSessionIOUnsupportedDeadlineDoesNotAcquireLease(t *testing.T) {
	m, r, _ := sessionIOFixture(t)
	w := httptest.NewRecorder()
	callSessionIO(m, w, r)
	if w.Code != 503 {
		t.Fatalf("unsupported writer status=%d", w.Code)
	}
	if sessionIOLeaseHeld(m.fence) {
		t.Fatal("unsupported writer retained lease")
	}
}

var _ io.ReadCloser = (*collaborationSessionIOBody)(nil)

// This wrapper supports deadlines but deliberately exposes no flush method.
type collaborationSessionNoFlush struct{ w *collaborationSessionIOWriter }

func (w *collaborationSessionNoFlush) Header() http.Header         { return w.w.Header() }
func (w *collaborationSessionNoFlush) WriteHeader(n int)           { w.w.WriteHeader(n) }
func (w *collaborationSessionNoFlush) Write(p []byte) (int, error) { return w.w.Write(p) }
func (w *collaborationSessionNoFlush) SetReadDeadline(v time.Time) error {
	return w.w.SetReadDeadline(v)
}
func (w *collaborationSessionNoFlush) SetWriteDeadline(v time.Time) error {
	return w.w.SetWriteDeadline(v)
}

type collaborationSessionUnwrap struct{ w http.ResponseWriter }

func (w *collaborationSessionUnwrap) Header() http.Header         { return w.w.Header() }
func (w *collaborationSessionUnwrap) WriteHeader(n int)           { w.w.WriteHeader(n) }
func (w *collaborationSessionUnwrap) Write(p []byte) (int, error) { return w.w.Write(p) }
func (w *collaborationSessionUnwrap) Unwrap() http.ResponseWriter { return w.w }
func TestMooringSessionIORequiresReadAndFlushSupport(t *testing.T) {
	for _, mode := range []string{"read", "flush"} {
		t.Run(mode, func(t *testing.T) {
			m, r, w := sessionIOFixture(t)
			var writer http.ResponseWriter = w
			if mode == "read" {
				w.unsupportedRead = true
			} else {
				writer = &collaborationSessionNoFlush{w}
			}
			callSessionIO(m, writer, r)
			if w.Code != 503 || w.writeHeld || w.flushes != 0 || w.setupHeld {
				t.Fatalf("unsupported transport acquired lease: status=%d writeHeld=%v flushes=%d", w.Code, w.writeHeld, w.flushes)
			}
			if !w.writeDeadline.IsZero() || (mode == "read" && !w.readDeadline.IsZero()) || (mode == "flush" && w.readDeadline.IsZero()) {
				t.Fatal("unsupported path retained deadline")
			}
		})
	}
}
func TestMooringSessionIOUnwrapAndOriginalBudget(t *testing.T) {
	m, r, w := sessionIOFixture(t)
	before := time.Now()
	callSessionIO(m, &collaborationSessionUnwrap{w}, r)
	after := time.Now()
	if w.Code != 403 || !w.flushHeld || w.flushes != 1 || w.setupHeld {
		t.Fatal("supported wrapper lost original denial/flush", w.Code)
	}
	if w.observedWrite.Before(before) || w.observedWrite.After(after.Add(12*time.Second)) || !w.observedWrite.Equal(w.observedRead) {
		t.Fatal("deadline differs from existing12s bootstrap ceiling")
	}
}
func TestMooringSessionIOStalledFlushRetainsLeaseUntilDeadline(t *testing.T) {
	m, r, w := sessionIOFixture(t)
	w.stalledFlush = true
	end := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(r.Context(), end)
	defer cancel()
	callSessionIO(m, w, r.WithContext(ctx))
	if w.flushes != 1 || !w.flushHeld || time.Now().Before(end) || len(w.pending) == 0 {
		t.Fatal("buffered flush was not bounded under lease")
	}
	if sessionIOLeaseHeld(m.fence) {
		t.Fatal("flush timeout retained lease")
	}
}
func TestMooringSessionIOExpiredRequestDoesNotReadBody(t *testing.T) {
	m, r, w := sessionIOFixture(t)
	body := &collaborationUnreadBody{}
	r.Body = body
	ctx, cancel := context.WithDeadline(r.Context(), time.Now().Add(-time.Second))
	defer cancel()
	callSessionIO(m, w, r.WithContext(ctx))
	if w.Code != 503 || body.reads != 0 || w.writeHeld || w.flushes != 0 {
		t.Fatal("expired request entered lease/body", w.Code, body.reads)
	}
}
func TestMooringSessionIOPreservesScopeAndBodyDenials(t *testing.T) {
	_, registry, _, ctx, _ := collaborationAdmissionFixture(t)
	m := &collaborationHTTPManager{pages: registry.pages, fence: registry.fence}
	ctx = context.WithValue(ctx, ctxWorkspaceID, registry.scope.WorkspaceID)
	for _, tc := range []struct {
		raw    string
		status int
	}{{"not JSON", 400}, {`{"unknown":true}`, 400}, {`{"checkpoint_base64":"?"}`, 400}} {
		r := httptest.NewRequest("POST", "/session", strings.NewReader(tc.raw)).WithContext(ctx)
		r.SetPathValue("slug", "admission-page")
		w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: m.fence}
		callSessionIO(m, w, r)
		if w.Code != tc.status || w.flushes != 1 || !w.flushHeld {
			t.Fatal("existing semantic denial changed", tc.raw, w.Code)
		}
	}
}

// Real net/http cleanup must not regain an unbounded read after an early
// pre-lease denial. The body deliberately supplies one of two declared bytes.
func TestMooringSessionIOPreLeaseDenialBoundsServerBodyDrain(t *testing.T) {
	m, _, _ := sessionIOFixture(t)
	returned := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 250*time.Millisecond)
		defer cancel()
		m.session(w, r.WithContext(ctx)) // Missing original stamp: pre-lease503.
		returned <- struct{}{}
	}))
	server.Config.ReadTimeout = 15 * time.Second
	server.Start()
	defer server.Close()
	c, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Fprintf(c, "POST /session HTTP/1.1\r\nHost: fixture.invalid\r\nContent-Length: 2\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "POST"})
	if response != nil {
		defer response.Body.Close()
		if response.StatusCode != 503 {
			t.Fatal(response.StatusCode)
		}
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("pre-lease denial erased read deadline before server unread-body drain")
	}
	// A deadline-closed connection may end before the HTTP status line.
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		t.Fatal(err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("handler did not execute its pre-lease denial")
	}
}

// net/http owns the next keepalive request's read deadline. Keeping the prior
// request's body-cleanup bound must not poison that next request.
func TestMooringSessionIORetainedReadDeadlineAllowsNextKeepalive(t *testing.T) {
	m, _, _ := sessionIOFixture(t)
	deadlines := make(chan time.Time, 2)
	server := httptest.NewUnstartedServer(m.fence.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 250*time.Millisecond)
		defer cancel()
		deadline, _ := ctx.Deadline()
		deadlines <- deadline
		m.session(w, r.WithContext(ctx))
	})))
	server.Config.ReadTimeout = 15 * time.Second
	server.Start()
	defer server.Close()
	c, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(c)
	for i := 0; i < 2; i++ {
		if _, err = fmt.Fprintf(c, "POST /session HTTP/1.1\r\nHost: fixture.invalid\r\nContent-Length: 2\r\n\r\n{}"); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, &http.Request{Method: "POST"})
		if err != nil {
			t.Fatal("same connection request", i, err)
		}
		_, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != 403 {
			t.Fatal("original no-source denial", i, response.StatusCode, readErr, closeErr)
		}
		deadline := <-deadlines
		if i == 0 {
			<-time.After(time.Until(deadline.Add(time.Millisecond)))
		}
	}
}
