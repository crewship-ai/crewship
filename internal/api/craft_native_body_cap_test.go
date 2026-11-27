package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/craftsession"
)

// The 16 MiB cap of the API stays for everything except the four routes that carry a whole native
// Sheet document, which enforce their own limits (import 64 MiB, the others 45 MiB).
func TestBodyCapExemptsOnlyTheNativeDocumentRoutes(t *testing.T) {
	for _, c := range []struct {
		method, path string
		exempt       bool
	}{
		{"POST", "/api/v1/pages/@book/craft/import", true},
		{"POST", "/api/v1/pages/book/craft/initialize", true},
		{"POST", "/api/v1/pages/@book/craft/document/save", true},
		{"POST", "/api/v1/pages/@book/craft/document/reconcile", false}, // carries no document
		{"GET", "/api/v1/pages/@book/craft/import", false},
		{"PUT", "/api/v1/pages/@book/craft/document/save", false},
		{"POST", "/api/v1/pages/@book/craft/import/abc/initialize", false}, // an empty body
		{"POST", "/api/v1/pages/@book/craft/document/restore", false},
		{"POST", "/api/v1/pages/@book/craft/document/share", false},
		{"POST", "/api/v1/pages/@book/craft/importx", false},
		{"POST", "/api/v1/pages/@book/craft/lease", false},
		{"POST", "/api/v1/pages/a/b/craft/import", false},
		{"POST", "/api/v1/pages//craft/import", false},
		{"POST", "/api/v1/pages/@book/craft/document/save/extra", false},
		{"POST", "/api/v1/other/@book/craft/import", false},
		{"POST", "/api/v1/pages/@book", false},
	} {
		if got := bodyCapExempt(httptest.NewRequest(c.method, c.path, nil)); got != c.exempt {
			t.Errorf("%s %s: exempt=%v want %v", c.method, c.path, got, c.exempt)
		}
	}
}

func TestBodyCapLetsABigNativeDocumentThroughAndStillStopsOtherRoutes(t *testing.T) {
	var read int64
	handler := BodyCap(maxAPIBodyBytes)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, 1<<30))
		read = n
		w.WriteHeader(http.StatusNoContent)
	}))
	send := func(method, path string, size int) int {
		read = 0
		req := httptest.NewRequest(method, path, strings.NewReader(strings.Repeat("x", size)))
		req.ContentLength = int64(size)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}
	const mib = 1 << 20
	for _, size := range []int{20 * mib, 50 * mib} {
		if code := send("POST", "/api/v1/pages/@book/craft/import", size); code != http.StatusNoContent || read != int64(size) {
			t.Errorf("import %d MiB: status %d, handler read %d", size/mib, code, read)
		}
	}
	if code := send("POST", "/api/v1/pages/@book/craft/initialize", 24*mib); code != http.StatusNoContent || read != 24*mib {
		t.Errorf("initialize 24 MiB: %d %d", code, read)
	}
	if code := send("POST", "/api/v1/pages/@book/craft/document/save", 30*mib); code != http.StatusNoContent || read != 30*mib {
		t.Errorf("save 30 MiB: %d %d", code, read)
	}
	if code := send("POST", "/api/v1/pages/@book/craft/document/reconcile", 17*mib); code != http.StatusRequestEntityTooLarge {
		t.Errorf("reconcile at 17 MiB: %d", code)
	}
	// everything else keeps the 16 MiB cap, announced or not
	if code := send("POST", "/api/v1/pages/@book/craft/document/share", 17*mib); code != http.StatusRequestEntityTooLarge {
		t.Errorf("another craft route at 17 MiB: %d", code)
	}
	if code := send("POST", "/api/v1/pages/@book/craft/import/abc/initialize", 17*mib); code != http.StatusRequestEntityTooLarge {
		t.Errorf("import initialize at 17 MiB: %d", code)
	}
	if code := send("POST", "/api/v1/missions", 17*mib); code != http.StatusRequestEntityTooLarge {
		t.Errorf("an ordinary route at 17 MiB: %d", code)
	}
}

// The import handler's own limits: 50 MiB reaches the converter, 65 MiB is refused 413, and the
// body is read only after the caller is authorized.
func TestCraftNativeImportReadsALargeWorkbookWithinItsOwnLimit(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", func(_ context.Context, data []byte) (*craftsession.ImportResult, error) {
		if len(data) != 50<<20 {
			t.Errorf("the converter got %d bytes", len(data))
		}
		return okConvert(nil, nil)
	})
	_ = f
	out := startImport(d, ctx, page, bytes.Repeat([]byte("x"), 50<<20))
	if out.Code != 202 {
		t.Fatalf("50 MiB: %d %s", out.Code, out.Body.String())
	}
	var started struct{ ID string }
	_ = json.Unmarshal(out.Body.Bytes(), &started)
	waitImportState(t, d, ctx, page, started.ID)
	if out = startImport(d, ctx, page, bytes.Repeat([]byte("x"), 65<<20)); out.Code != 413 {
		t.Fatalf("65 MiB: %d", out.Code)
	}
}

func TestCraftNativeImportRefusesAnUnauthorizedCallerBeforeReadingTheBody(t *testing.T) {
	d, _, ctx, page := importFixture(t, "nonowner-writer", okConvert)
	body := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte("x"), 50<<20))}
	req := httptest.NewRequest("POST", "/x", body).WithContext(ctx)
	req.SetPathValue("slug", "@"+page)
	out := httptest.NewRecorder()
	d.importStart(out, req)
	if out.Code != 403 || body.n != 0 {
		t.Fatalf("status %d after reading %d bytes", out.Code, body.n)
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Initialize refuses before reading its body when the administrator turned both applications off:
// the application named inside the body could only be refused too.
func TestCraftNativeInitializeRefusesTurnedOffApplicationsBeforeReadingTheBody(t *testing.T) {
	d, f, ctx, page := importFixture(t, "owner", okConvert)
	d.human.apps = newCraftAppsGate(&craftapps.Policy{}, f.db) // both applications are off by default
	body := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte("x"), 8<<20))}
	req := httptest.NewRequest("POST", "/x", body).WithContext(ctx)
	req.SetPathValue("slug", "@"+page)
	out := httptest.NewRecorder()
	d.initialize(out, req)
	if out.Code != 503 || body.n != 0 {
		t.Fatalf("status %d after reading %d bytes", out.Code, body.n)
	}
}
