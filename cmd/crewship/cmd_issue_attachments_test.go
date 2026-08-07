package main

// CLI issue attachments — the two CodeRabbit findings on #1791.
//
//  3. the upload limit was checked against os.Stat and then not enforced while
//     reading, so a file that changed size between the two was buffered whole;
//  4. the download's WRITABLE file handle was closed by a bare defer, which is
//     where a short write or a full disk is reported and nowhere else.
//
// Both are TOCTOU-shaped, so both tests exercise the real command and let the
// state change underneath it rather than asserting on how the code is written.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// shrinkAttachmentLimit lowers the CLI's ceiling for one test.
func shrinkAttachmentLimit(t *testing.T, limit int64) {
	t.Helper()
	orig := attachmentUploadLimit
	attachmentUploadLimit = limit
	t.Cleanup(func() { attachmentUploadLimit = orig })
}

// attachTestServer stands up the two endpoints `crewship issue attach` calls, in
// the order it calls them, and hands back the recorded upload body.
//
// onIssueLookup runs INSIDE the issue-lookup handler — i.e. after the command
// has stat'd the file and before it opens it. That is the TOCTOU window, and
// this is what lets a test open it deterministically instead of racing a
// goroutine.
type attachTestServer struct {
	uploads   int
	lastBody  []byte
	lastName  string
	server    *httptest.Server
	uploadErr int
}

func newAttachTestServer(t *testing.T, onIssueLookup func()) *attachTestServer {
	t.Helper()
	s := &attachTestServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/issues/"):
			if onIssueLookup != nil {
				onIssueLookup()
			}
			ident := "ENG-4"
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "mission-1", "crew_id": "crew-1", "identifier": &ident, "title": "t",
			})
		case strings.HasSuffix(r.URL.Path, "/attachments") && r.Method == http.MethodPost:
			s.uploads++
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer file.Close()
			buf := make([]byte, 1<<20)
			n, _ := file.Read(buf)
			s.lastBody = append(s.lastBody[:0], buf[:n]...)
			s.lastName = header.Filename
			if s.uploadErr != 0 {
				http.Error(w, "nope", s.uploadErr)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "att-1", "filename": header.Filename, "size_bytes": n,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

// loginAgainst points the CLI's package-level state at a stub server.
func loginAgainst(t *testing.T, url string) {
	t.Helper()
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs", // CUID-shaped: no slug resolution round trip
		Server:    url,
	}
}

// ── FINDING 3: the limit is enforced while READING ────────────────────────

// A file that grows past the ceiling after the size check is refused, and
// nothing is uploaded.
//
// The growth happens inside the issue-lookup handler, which the command calls
// between os.Stat and os.Open — the real window, opened deterministically. A log
// file being appended to hits this without anybody being hostile; a path an
// attacker can write to hits it on purpose.
func TestIssueAttach_RefusesAFileThatGrowsAfterTheStat(t *testing.T) {
	const limit = 64
	shrinkAttachmentLimit(t, limit)

	path := filepath.Join(t.TempDir(), "grow.log")
	if err := os.WriteFile(path, []byte("small enough at stat time\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	grown := false
	srv := newAttachTestServer(t, func() {
		// Between the stat and the read: the file is now well over the limit.
		if err := os.WriteFile(path, []byte(strings.Repeat("x", limit*4)), 0o600); err != nil {
			t.Errorf("grow file: %v", err)
			return
		}
		grown = true
	})
	loginAgainst(t, srv.server.URL)

	err := issueAttachCmd.RunE(issueAttachCmd, []string{"ENG-4", path})
	if !grown {
		t.Fatal("the file never grew — the test did not open the window it is about")
	}
	if err == nil {
		t.Fatalf("attach succeeded on a file %d bytes over the %d-byte limit — the ceiling is "+
			"checked against a stat and then not enforced while reading, so the command buffers "+
			"whatever it is handed", limit*4-limit, limit)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error does not name the limit: %v", err)
	}
	if srv.uploads != 0 {
		t.Errorf("the oversized file was uploaded anyway (%d POST(s))", srv.uploads)
	}
}

// The obvious refusal still works, and still happens before the network.
func TestIssueAttach_RefusesAFileAlreadyOverTheLimit(t *testing.T) {
	shrinkAttachmentLimit(t, 64)
	path := filepath.Join(t.TempDir(), "big.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("y", 300)), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	srv := newAttachTestServer(t, nil)
	loginAgainst(t, srv.server.URL)

	err := issueAttachCmd.RunE(issueAttachCmd, []string{"ENG-4", path})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error = %v, want the local size refusal", err)
	}
	if srv.uploads != 0 {
		t.Errorf("POSTed anyway (%d)", srv.uploads)
	}
}

// A file at EXACTLY the ceiling is legal and must still upload whole.
//
// The bounded read is limit+1 for this reason: a LimitReader of `limit` would
// silently truncate the last byte of a legal file and the server would store a
// corrupt copy under a digest nobody can reproduce.
func TestIssueAttach_UploadsAFileAtExactlyTheLimit(t *testing.T) {
	const limit = 64
	shrinkAttachmentLimit(t, limit)

	path := filepath.Join(t.TempDir(), "exact.log")
	content := []byte(strings.Repeat("z", limit))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	srv := newAttachTestServer(t, nil)
	loginAgainst(t, srv.server.URL)

	if err := issueAttachCmd.RunE(issueAttachCmd, []string{"ENG-4", path}); err != nil {
		t.Fatalf("attach at exactly the limit failed: %v", err)
	}
	if srv.uploads != 1 {
		t.Fatalf("uploads = %d, want 1", srv.uploads)
	}
	if string(srv.lastBody) != string(content) {
		t.Errorf("uploaded %d bytes, want %d — the bounded read truncated a legal file",
			len(srv.lastBody), len(content))
	}
	if srv.lastName != "exact.log" {
		t.Errorf("filename = %q", srv.lastName)
	}
}

// ── FINDING 4: the download's writable handle ─────────────────────────────

// downloadServer serves one attachment body, optionally lying about its length.
func downloadServer(t *testing.T, body []byte, lieContentLength int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/issues/") {
			ident := "ENG-4"
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "mission-1", "crew_id": "crew-1", "identifier": &ident, "title": "t",
			})
			return
		}
		if lieContentLength > 0 {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", lieContentLength))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

// A download that fails mid-body leaves no file behind.
//
// The old code returned io.Copy's error through a `defer out.Close()` that threw
// its own error away, and left the partial file on disk under the name the user
// asked for. Whatever is written must be removed, the way `crewship backup
// download` removes a half-written bundle: a truncated file that looks complete
// is worse than no file.
func TestIssueAttachment_Download_LeavesNoPartialFileWhenTheBodyIsTruncated(t *testing.T) {
	body := []byte("the first half of a log file")
	srv := downloadServer(t, body, len(body)+4096) // claims more than it sends
	loginAgainst(t, srv.URL)

	out := filepath.Join(t.TempDir(), "out.log")
	attachmentOutPath = out
	t.Cleanup(func() { attachmentOutPath = "" })

	err := issueAttachmentCmd.RunE(issueAttachmentCmd, []string{"ENG-4", "att-1"})
	if err == nil {
		t.Fatal("a body that ended early was reported as a successful download")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("a partial file was left at %s (%v) — it looks like the attachment and is not", out, statErr)
	}
}

// A body over the ceiling is refused rather than written one byte short.
//
// io.LimitReader(body, limit+1) bounds what is read; without a check on the
// count that bound writes limit+1 bytes of a larger file and calls it a success.
func TestIssueAttachment_Download_RefusesAnOversizedBody(t *testing.T) {
	const limit = 64
	shrinkAttachmentLimit(t, limit)

	srv := downloadServer(t, []byte(strings.Repeat("w", limit*4)), 0)
	loginAgainst(t, srv.URL)

	out := filepath.Join(t.TempDir(), "out.log")
	attachmentOutPath = out
	t.Cleanup(func() { attachmentOutPath = "" })

	err := issueAttachmentCmd.RunE(issueAttachmentCmd, []string{"ENG-4", "att-1"})
	if err == nil {
		t.Fatal("an over-limit body was written out truncated and reported as a success")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("the truncated file survived at %s (%v)", out, statErr)
	}
}

// The ordinary download still writes the bytes and keeps the file.
func TestIssueAttachment_Download_WritesTheFile(t *testing.T) {
	body := []byte("a complete, small attachment\n")
	srv := downloadServer(t, body, 0)
	loginAgainst(t, srv.URL)

	out := filepath.Join(t.TempDir(), "out.log")
	attachmentOutPath = out
	t.Cleanup(func() { attachmentOutPath = "" })

	if err := issueAttachmentCmd.RunE(issueAttachmentCmd, []string{"ENG-4", "att-1"}); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("wrote %q, want %q", got, body)
	}
}
