package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/localfs"
)

// Chat attachments have never worked on a provisioned crew.
//
// The composer PUTs to /crews/{id}/files/save with
// path=<crewID>/<agentSlug>/attachments/<chatId>/<file>, which resolves to the
// agent /output tree. After a crew is provisioned that tree is owned by the
// agent uid 1001 (prepareCrewDirs chowns it; the agent creates <slug>/ at 0755),
// so the server uid cannot create <slug>/attachments and localfs fails with
//
//	create parent dir: mkdirat <crew>/<agent>/attachments: permission denied
//
// #922's container replay was supposed to cover exactly this, but it was
// written for — and gated on — the crew SHARED tree only, so for an /output
// key it was unreachable code: handleFileSave returned at the !isShared branch
// before ever reaching it. These tests pin the reachable version.
//
// The 1001 ownership itself cannot be reproduced without root, so the seam is
// a real chmod 0555 on the agent directory: the same EACCES from the same
// syscall, from a real localfs, through the real route. What it proves is the
// server's reaction to an unwritable agent tree. What it does not prove is that
// uid 1001 is what made it unwritable.
func TestHandleFileSave_AttachmentIntoCrewOwnedOutputTree(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission bits are advisory for root")
	}

	const (
		urlPath = "/crews/crewX/files/save?path=crewX%2Falex%2Fattachments%2Fchat-1%2Fbook.xlsx"
		content = "PK\x03\x04not-really-a-spreadsheet"
		wantDst = "/output/alex/attachments/chat-1/book.xlsx"
	)

	cases := []struct {
		name string
		// ctr is the container provider the server is built with; nil means
		// this deployment has none configured.
		ctr        func() *recordingContainer
		wantStatus int
		// wantErrContains is checked against the response body. It is the text
		// a user actually reads: proxy_attachments.go forwards the IPC error
		// through to the composer toast.
		wantErrContains []string
		wantDest        string // DEST env on the exec; "" means "no exec at all"
	}{
		{
			name:       "running crew: the write is replayed through the container as the tree owner",
			ctr:        func() *recordingContainer { return &recordingContainer{mockContainer: &mockContainer{}} },
			wantStatus: http.StatusOK,
			wantDest:   wantDst,
		},
		{
			name: "stopped crew: 409 naming the attachment and what to do",
			ctr: func() *recordingContainer {
				return &recordingContainer{mockContainer: &mockContainer{}, execErr: errors.New("container not running")}
			},
			// The IPC message stays accurate for every writer of this tree
			// (the file editor and `crewship agent file-write` use the same
			// route). Naming the ATTACHMENT is the API layer's job, since only
			// it knows that is what the caller was doing — see
			// TestAgentChatAttachment_ForwardsIPCErrorLegibly.
			wantStatus:      http.StatusConflict,
			wantErrContains: []string{"output directory", "start the crew"},
		},
		{
			name: "container write fails: not a false success",
			ctr: func() *recordingContainer {
				return &recordingContainer{mockContainer: &mockContainer{}, exitCode: 1}
			},
			wantStatus:      http.StatusInternalServerError,
			wantErrContains: []string{"failed to save file"},
			wantDest:        wantDst,
		},
		{
			name:       "no container runtime configured: say so, do not just fail",
			ctr:        nil,
			wantStatus: http.StatusServiceUnavailable,
			wantErrContains: []string{
				"owned by the crew runtime",
				"container runtime",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			base, err := localfs.New(root)
			if err != nil {
				t.Fatal(err)
			}
			// The agent's own directory exists (the agent made it inside the
			// container) but the server cannot write into it.
			agentDir := filepath.Join(root, "crewX", "alex")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(agentDir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(agentDir, 0o755) })

			var ctr *recordingContainer
			s := newContainerFallbackServer(t, base, nil)
			if tc.ctr != nil {
				ctr = tc.ctr()
				s = newContainerFallbackServer(t, base, ctr)
			}

			req := httptest.NewRequest("PUT", urlPath, strings.NewReader(content))
			rec := httptest.NewRecorder()
			s.ipcMux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			for _, want := range tc.wantErrContains {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("body %s should contain %q", rec.Body.String(), want)
				}
			}
			if ctr == nil {
				return
			}
			if tc.wantDest == "" {
				if ctr.gotCfg.User != "" {
					t.Errorf("no exec expected, but one ran: %+v", ctr.gotCfg)
				}
				return
			}
			if ctr.gotCfg.User != "1001:1001" {
				t.Errorf("exec User = %q, want 1001:1001 (the owner of the output tree)", ctr.gotCfg.User)
			}
			env := map[string]string{}
			for _, e := range ctr.gotCfg.Env {
				k, v, _ := strings.Cut(e, "=")
				env[k] = v
			}
			if env["DEST"] != tc.wantDest {
				t.Errorf("DEST = %q, want %q", env["DEST"], tc.wantDest)
			}
			// The in-container write must be fenced to /output, not to the
			// shared tree it was originally written for.
			if env["FENCE"] != "/output" {
				t.Errorf("FENCE = %q, want /output", env["FENCE"])
			}
			script := strings.Join(ctr.gotCfg.Cmd, " ")
			if !strings.Contains(script, "realpath") {
				t.Errorf("container script lost its realpath fence: %q", script)
			}
			if got := string(ctr.gotStdin); got != content {
				t.Errorf("exec stdin = %q, want the uploaded bytes %q", got, content)
			}
		})
	}
}

// captureReader is what lets the /output write stay a stream and still be
// replayable. Its contract is easy to get subtly wrong — the consumer may have
// read all, some, or none of the body before the write failed — so it is pinned
// on its own rather than only through the handler.
func TestCaptureReader(t *testing.T) {
	const limit = 16

	cases := []struct {
		name string
		body string
		// consume is how many bytes the storage write pulled before failing.
		// -1 means "everything".
		consume  int
		wantOK   bool
		wantBody string
	}{
		{
			// The field case: localfs fails at mkdirat, so it never touches
			// the reader and the capture is empty when the replay starts.
			name: "consumer read nothing", body: "hello", consume: 0,
			wantOK: true, wantBody: "hello",
		},
		{
			name: "consumer read part of the body", body: "hello world", consume: 4,
			wantOK: true, wantBody: "hello world",
		},
		{
			name: "consumer read the whole body", body: "hello", consume: -1,
			wantOK: true, wantBody: "hello",
		},
		{
			name: "empty body", body: "", consume: -1,
			wantOK: true, wantBody: "",
		},
		{
			name: "exactly at the limit is still replayable", body: strings.Repeat("x", limit), consume: 0,
			wantOK: true, wantBody: strings.Repeat("x", limit),
		},
		{
			// One byte past: the capture is dropped rather than replayed
			// truncated. Losing the replay is recoverable; writing half a file
			// into the agent's tree is not.
			name: "one byte past the limit is not replayable", body: strings.Repeat("x", limit+1), consume: 0,
			wantOK: false,
		},
		{
			name: "grown past the limit after a partial read", body: strings.Repeat("x", limit+1), consume: 8,
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &captureReader{r: strings.NewReader(tc.body), limit: limit}
			if tc.consume < 0 {
				if _, err := io.Copy(io.Discard, c); err != nil {
					t.Fatal(err)
				}
			} else if tc.consume > 0 {
				if _, err := io.ReadFull(c, make([]byte, tc.consume)); err != nil {
					t.Fatal(err)
				}
			}

			got, ok := c.replay()
			if ok != tc.wantOK {
				t.Fatalf("replay ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && string(got) != tc.wantBody {
				t.Errorf("replay = %q, want %q", got, tc.wantBody)
			}
		})
	}
}

// A read error on the unread remainder must be reported, never replayed as a
// short body — that would write a truncated file and call it success.
func TestCaptureReader_ReadErrorIsNotAShortBody(t *testing.T) {
	c := &captureReader{
		r:     io.MultiReader(strings.NewReader("head"), &errReader{err: errors.New("connection reset")}),
		limit: 1024,
	}
	if _, ok := c.replay(); ok {
		t.Fatal("replay reported success despite a read error on the remainder")
	}
}

type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }

// TestHandleFileSave_OutputTreeCrewMissing: the replay needs a crew row to
// resolve a container name. A save for a crew that isn't there is a 404, not a
// 500 — the same mapping the shared tree already used.
func TestHandleFileSave_OutputTreeCrewMissing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission bits are advisory for root")
	}
	root := t.TempDir()
	base, err := localfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(root, "crewGONE", "alex")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(agentDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(agentDir, 0o755) })

	s := newContainerFallbackServer(t, base, &recordingContainer{mockContainer: &mockContainer{}})
	req := httptest.NewRequest("PUT",
		"/crews/crewGONE/files/save?path=crewGONE%2Falex%2Fattachments%2Fc1%2Fx.txt",
		strings.NewReader("x"))
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleFileSave_OutputTooLargeToReplay: the /output stream stays uncapped
// (TestHandleFileSave_OutputStreamsUncapped pins that), which means a body
// larger than the replay buffer cannot be replayed through the container. That
// has to be an honest error naming the limit, not a silent truncation and not a
// bare "failed to save file".
func TestHandleFileSave_OutputTooLargeToReplay(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission bits are advisory for root")
	}
	root := t.TempDir()
	base, err := localfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(root, "crewX", "alex")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(agentDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(agentDir, 0o755) })

	ctr := &recordingContainer{mockContainer: &mockContainer{}}
	s := newContainerFallbackServer(t, base, ctr)

	body := io.LimitReader(zeroReader{}, maxCrewFileSaveBytes+1)
	req := httptest.NewRequest("PUT",
		"/crews/crewX/files/save?path=crewX%2Falex%2Fattachments%2Fc1%2Fhuge.bin", body)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "crew container") {
		t.Errorf("413 body should explain that the container route has a size limit: %s", rec.Body.String())
	}
	if ctr.gotStdin != nil {
		t.Errorf("a body that cannot be replayed must not reach the container")
	}
}
