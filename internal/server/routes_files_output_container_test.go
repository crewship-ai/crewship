package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
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
	// SKIP-WAIVER(#1977): the setup is a chmod 0555 that root ignores, so as

	// uid 0 this test would pass while proving nothing — the assertion needs a

	// real kernel EACCES to have anything to observe.

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
	// SKIP-WAIVER(#1977): the setup is a chmod 0555 that root ignores, so as

	// uid 0 this test would pass while proving nothing — the assertion needs a

	// real kernel EACCES to have anything to observe.

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
	// SKIP-WAIVER(#1977): the setup is a chmod 0555 that root ignores, so as

	// uid 0 this test would pass while proving nothing — the assertion needs a

	// real kernel EACCES to have anything to observe.

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

// ── the delete half of the same replay ─────────────────────────────────────

// permDeleteStorage delegates to a real localfs but forces an EACCES on Delete
// for one key — the #922 ownership handoff seen from the removal side. After a
// crew is provisioned the /output tree is owned by uid 1001, and unlinking an
// entry needs write on its PARENT directory, which 1001 now owns; the server uid
// gets permission denied without ever touching the entry itself.
type permDeleteStorage struct {
	provider.StorageProvider
	failKey string
}

func (p *permDeleteStorage) Delete(ctx context.Context, path string) error {
	if path == p.failKey {
		return &fs.PathError{Op: "unlinkat", Path: path, Err: fs.ErrPermission}
	}
	return p.StorageProvider.Delete(ctx, path)
}

// shellExecContainer runs the container half of the replay FOR REAL: it takes
// the script the server sends, maps the two container-absolute paths onto a host
// directory standing in for the bind mount, and executes it with /bin/sh.
//
// recordingContainer cannot answer the question these tests ask. It reports exit
// 0 for whatever it is handed, so a script that could not possibly remove the
// destination still looks like a success — which is exactly the shape of the bug
// (`rm -f` on a directory exits non-zero, `set -eu` propagates, the endpoint
// 5xxs and the caller can never delete the attachment). What the shell double
// does not reproduce is the uid: it runs as the test's own user, so it proves
// what the SCRIPT does, not what 1001 is allowed to do.
type shellExecContainer struct {
	*mockContainer
	// roots maps a container fence root (/output, /crew/shared) to the host
	// directory that stands in for what is bound there.
	roots    map[string]string
	gotCfg   provider.ExecConfig
	output   string
	exitCode int
}

func (c *shellExecContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.gotCfg = cfg
	env := map[string]string{}
	for _, e := range cfg.Env {
		k, v, _ := strings.Cut(e, "=")
		env[k] = v
	}
	fence, dest := env["FENCE"], env["DEST"]
	host, ok := c.roots[fence]
	if !ok {
		return nil, fmt.Errorf("test double has no host tree for fence %q", fence)
	}
	if dest != fence && !strings.HasPrefix(dest, fence+"/") {
		return nil, fmt.Errorf("DEST %q is not under FENCE %q", dest, fence)
	}
	hostDest := host + strings.TrimPrefix(dest, fence)

	cmd := exec.Command(cfg.Cmd[0], cfg.Cmd[1:]...) // #nosec G204 — the script under test
	cmd.Env = append(os.Environ(), "DEST="+hostDest, "FENCE="+host)
	if cfg.Stdin != nil {
		cmd.Stdin = cfg.Stdin
	}
	out, err := cmd.CombinedOutput()
	c.output = string(out)
	if cmd.ProcessState == nil {
		// The shell never started — that is a broken test host, not a verdict
		// on the script, so it must not read as "the removal failed".
		return nil, fmt.Errorf("run %v: %w", cfg.Cmd, err)
	}
	c.exitCode = cmd.ProcessState.ExitCode()
	return &provider.ExecResult{ExecID: "e-shell", Reader: io.NopCloser(bytes.NewReader(out))}, nil
}

func (c *shellExecContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, c.exitCode, nil
}

// A chat attachment lives at attachments/<chatId>/<attachmentId>/<filename>, so
// deleting one removes the attachment's own DIRECTORY — that is what leaves no
// empty directory behind in a tree the agent reads. Host-side that is a
// RemoveAll and it works; on a provisioned crew the host is refused and the
// removal is replayed through the container, and the replay has to be able to
// remove the same thing the host would have.
//
// It could not: the in-container script ran `rm -f "$DEST"`, which exits 1 on a
// directory, `set -eu` turned that into a failed exec, and the endpoint answered
// 5xx. The attachment was undeletable and every retry failed identically.
func TestHandleFileDelete_ReplayRemovesWhatTheHostWouldHave(t *testing.T) {
	requireShellTools(t)

	const (
		chatDirKey = "crewX/alex/attachments/chat-1"
		attDirKey  = chatDirKey + "/att-9"
		legacyKey  = chatDirKey + "/old-report.pdf"
	)

	cases := []struct {
		name string
		// key is both the storage key the host is refused on and the ?path=
		// the route is asked for.
		key        string
		wantStatus int
		gone       []string // relative to the storage root
		kept       []string
	}{
		{
			name:       "the attachment's own directory",
			key:        attDirKey,
			wantStatus: http.StatusOK,
			gone:       []string{attDirKey},
			kept:       []string{chatDirKey, chatDirKey + "/att-8/other.pdf"},
		},
		{
			name:       "a legacy attachment, which is a plain file",
			key:        legacyKey,
			wantStatus: http.StatusOK,
			gone:       []string{legacyKey},
			kept:       []string{chatDirKey, attDirKey + "/book.xlsx"},
		},
		{
			name:       "bytes that are already gone stay a success",
			key:        chatDirKey + "/att-never",
			wantStatus: http.StatusOK,
			kept:       []string{attDirKey + "/book.xlsx"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			seedCrewTreeForDelete(t, root)
			base, err := localfs.New(root)
			if err != nil {
				t.Fatal(err)
			}
			ctr := &shellExecContainer{
				mockContainer: &mockContainer{},
				roots: map[string]string{
					containerOutputRoot:     filepath.Join(root, "crewX"),
					containerCrewSharedRoot: filepath.Join(root, "crews", "crewX", "shared"),
				},
			}
			s := newContainerFallbackServer(t,
				&permDeleteStorage{StorageProvider: base, failKey: tc.key}, ctr)

			req := httptest.NewRequest("DELETE", "/crews/crewX/files/delete?path="+tc.key, nil)
			rec := httptest.NewRecorder()
			s.ipcMux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s; container said: %s",
					rec.Code, tc.wantStatus, rec.Body.String(), ctr.output)
			}
			if ctr.gotCfg.User != "1001:1001" {
				t.Fatalf("exec User = %q, want 1001:1001 (the owner of the provisioned tree)", ctr.gotCfg.User)
			}
			for _, rel := range tc.gone {
				if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
					t.Errorf("%s survived the replay (%v) — the row that names it is about to go, "+
						"and nothing else walks this tree", rel, err)
				}
			}
			for _, rel := range tc.kept {
				if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
					t.Errorf("the replay removed %s, which was not asked for: %v", rel, err)
				}
			}
		})
	}
}

// The widening above has a floor: the replay removes a subtree, so it must never
// be handed the tree ROOT. Host-side a delete of `shared` is a RemoveAll of the
// crew's whole shared tree; through the container that would now be one `rm -rf`
// away, so the script refuses it by name.
func TestHandleFileDelete_ReplayRefusesTheTreeRoot(t *testing.T) {
	requireShellTools(t)

	root := t.TempDir()
	seedCrewTreeForDelete(t, root)
	base, err := localfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctr := &shellExecContainer{
		mockContainer: &mockContainer{},
		roots: map[string]string{
			containerOutputRoot:     filepath.Join(root, "crewX"),
			containerCrewSharedRoot: filepath.Join(root, "crews", "crewX", "shared"),
		},
	}
	s := newContainerFallbackServer(t,
		&permDeleteStorage{StorageProvider: base, failKey: "crews/crewX/shared"}, ctr)

	req := httptest.NewRequest("DELETE", "/crews/crewX/files/delete?path=shared", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("removing the shared tree root through the container reported success")
	}
	// And what the caller reads names the operation it asked for: this text is
	// forwarded verbatim into the composer's toast.
	if !strings.Contains(rec.Body.String(), "delete") {
		t.Errorf("body %s should say which operation failed", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "crews", "crewX", "shared", "scripts", "probe.sh")); err != nil {
		t.Errorf("the crew's shared tree was removed through the replay: %v", err)
	}
}

// requireShellTools skips a test that runs the container script for real when
// the host cannot run it. The script is POSIX sh plus realpath — what the crew
// image has — and a host missing either says nothing about the script.
func requireShellTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"sh", "realpath"} {
		if _, err := exec.LookPath(bin); err != nil {
			// SKIP-WAIVER(#1977): this test runs the real container script through
			// the host's shell, which is the only way to catch what a mock cannot —
			// `rm -f` refusing a directory. A host without sh or realpath can say
			// nothing about a script that runs inside the crew image.
			t.Skipf("%s is not available: %v", bin, err)
		}
	}
}

// seedCrewTreeForDelete lays out both crew trees as a provisioned crew has them:
// two chat attachments in their own per-id directories, one legacy attachment
// beside them at the old two-segment path, and a file in the shared tree.
func seedCrewTreeForDelete(t *testing.T, root string) {
	t.Helper()
	for _, f := range []struct{ path, content string }{
		{"crewX/alex/attachments/chat-1/att-9/book.xlsx", "PK\x03\x04"},
		{"crewX/alex/attachments/chat-1/att-8/other.pdf", "%PDF-1.4"},
		{"crewX/alex/attachments/chat-1/old-report.pdf", "%PDF-1.3"},
		{"crews/crewX/shared/scripts/probe.sh", "#!/bin/sh\n"},
	} {
		full := filepath.Join(root, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The premise the replay rests on, with real permission bits rather than a
// double: on a tree the server uid cannot write, removing an attachment's
// directory fails with a PERMISSION error — which is what routes the delete
// through the container in the first place. Nothing else in this file proves the
// host actually refuses; a different errno would leave the fallback unreachable
// and the 500 would be the only thing anyone saw.
func TestHandleFileDelete_HostRefusalIsWhatTriggersTheReplay(t *testing.T) {
	// SKIP-WAIVER(#1977): the setup is a chmod 0555 that root ignores, so as

	// uid 0 this test would pass while proving nothing — the assertion needs a

	// real kernel EACCES to have anything to observe.

	if os.Getuid() == 0 {
		t.Skip("permission bits are advisory for root")
	}
	root := t.TempDir()
	seedCrewTreeForDelete(t, root)
	base, err := localfs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	chatDir := filepath.Join(root, "crewX", "alex", "attachments", "chat-1")
	if err := os.Chmod(chatDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(chatDir, 0o755) })

	// The host attempt on its own: this is the error handleFileDelete sees.
	hostErr := base.Delete(context.Background(), "crewX/alex/attachments/chat-1/att-9")
	if !errors.Is(hostErr, fs.ErrPermission) {
		t.Fatalf("host delete of a per-attachment directory under an unwritable parent = %v, "+
			"want a permission error — the container fallback is gated on fs.ErrPermission", hostErr)
	}

	ctr := &recordingContainer{mockContainer: &mockContainer{}}
	s := newContainerFallbackServer(t, base, ctr)
	req := httptest.NewRequest("DELETE",
		"/crews/crewX/files/delete?path=crewX%2Falex%2Fattachments%2Fchat-1%2Fatt-9", nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	env := map[string]string{}
	for _, e := range ctr.gotCfg.Env {
		k, v, _ := strings.Cut(e, "=")
		env[k] = v
	}
	if env["DEST"] != "/output/alex/attachments/chat-1/att-9" {
		t.Errorf("DEST = %q, want the attachment's directory inside the container", env["DEST"])
	}
	if env["FENCE"] != containerOutputRoot {
		t.Errorf("FENCE = %q, want %s", env["FENCE"], containerOutputRoot)
	}
}

// The fence check compared a RESOLVED path against an UNRESOLVED one.
//
// crewFileDeleteScript resolves the destination's parent with realpath, then
// tests that result against $FENCE as it was handed in. Those are only
// comparable when no component of the fence is a symlink. The moment one is,
// realpath returns the link's target, the target does not begin with the fence
// as spelled, and the script refuses a removal that is squarely inside the
// tree — every removal, permanently, not an edge case.
//
// macOS CI is where it surfaced: t.TempDir() there sits under /var, which is a
// symlink to /private/var, so `Go (macos-arm64)` failed all three replay cases
// with "refuse: destination escapes ...". Nothing about that is macOS-specific
// — this test builds the same shape on any platform by pointing the fence at a
// symlink — and nothing about it is test-only either: a deployment whose bind
// mount is reached through a link would have had an undeletable attachment
// tree, with the refusal blaming the destination for the fence's spelling.
func TestCrewFileDeleteScript_FenceReachedThroughSymlink(t *testing.T) {
	requireShellTools(t)

	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	link := filepath.Join(tmp, "link")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	// `link` stands in for /var -> /private/var: same tree, spelled through a
	// symlink, which is what the fence is handed.
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err) // SKIP-WAIVER(#1977)
	}
	seedCrewTreeForDelete(t, real)

	const attDirKey = "crewX/alex/attachments/chat-1/att-9"

	base, err := localfs.New(real)
	if err != nil {
		t.Fatal(err)
	}
	ctr := &shellExecContainer{
		mockContainer: &mockContainer{},
		// The fence points at the symlinked spelling; the bytes are the same.
		roots: map[string]string{
			containerOutputRoot:     filepath.Join(link, "crewX"),
			containerCrewSharedRoot: filepath.Join(link, "crews", "crewX", "shared"),
		},
	}
	s := newContainerFallbackServer(t,
		&permDeleteStorage{StorageProvider: base, failKey: attDirKey}, ctr)

	req := httptest.NewRequest("DELETE", "/crews/crewX/files/delete?path="+attDirKey, nil)
	rec := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s; container said: %s",
			rec.Code, rec.Body.String(), ctr.output)
	}
	if _, err := os.Stat(filepath.Join(real, attDirKey)); !os.IsNotExist(err) {
		t.Errorf("%s survived the replay (%v)", attDirKey, err)
	}
	// The siblings the removal had no business touching.
	for _, rel := range []string{
		"crewX/alex/attachments/chat-1",
		"crewX/alex/attachments/chat-1/att-8/other.pdf",
	} {
		if _, err := os.Stat(filepath.Join(real, rel)); err != nil {
			t.Errorf("%s should have survived: %v", rel, err)
		}
	}
}

// The tree root stays unremovable when the fence is spelled through a symlink.
//
// Resolving the fence must not cost the guard that keeps `rm -rf` off the root
// itself: a DEST equal to the fence — by either spelling — is refused, and the
// tree is still there afterwards.
func TestCrewFileDeleteScript_RootRefusedThroughSymlink(t *testing.T) {
	requireShellTools(t)

	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	link := filepath.Join(tmp, "link")
	if err := os.MkdirAll(filepath.Join(real, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "keep", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err) // SKIP-WAIVER(#1977)
	}

	for _, dest := range []string{link, link + "/", real} {
		cmd := exec.Command("sh", "-c", crewFileDeleteScript) // #nosec G204 — the script under test
		cmd.Env = append(os.Environ(), "DEST="+dest, "FENCE="+link)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("DEST=%q was accepted; the script must never remove the tree root (output: %s)", dest, out)
		}
		if _, serr := os.Stat(filepath.Join(real, "keep", "f.txt")); serr != nil {
			t.Fatalf("DEST=%q removed the tree: %v", dest, serr)
		}
	}
}

// A destination that resolves OUTSIDE the fence is still refused after the
// fence itself is resolved — the containment property the script exists for.
func TestCrewFileDeleteScript_EscapeStillRefused(t *testing.T) {
	requireShellTools(t)

	tmp := t.TempDir()
	fence := filepath.Join(tmp, "fence")
	outside := filepath.Join(tmp, "outside")
	for _, d := range []string{fence, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlinked component inside the fence that leads out of it — the attack
	// the in-container realpath check is there to stop.
	if err := os.Symlink(outside, filepath.Join(fence, "escape")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err) // SKIP-WAIVER(#1977)
	}

	cmd := exec.Command("sh", "-c", crewFileDeleteScript) // #nosec G204 — the script under test
	cmd.Env = append(os.Environ(), "DEST="+filepath.Join(fence, "escape", "secret.txt"), "FENCE="+fence)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a destination outside the fence was accepted (output: %s)", out)
	}
	if _, serr := os.Stat(secret); serr != nil {
		t.Fatalf("the file outside the fence was removed: %v", serr)
	}
}
