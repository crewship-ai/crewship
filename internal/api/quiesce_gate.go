package api

import (
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

// The HTTP side of the instance backup's quiet window and of the automation
// holds an instance restore leaves (internal/quiesce).
//
//   - Every mutating request is a writer in the quiet window's barrier: it
//     enters the gate before its handler runs and leaves when the handler
//     returns. While a window is closing or held, entry is answered 503 with
//     Retry-After, so no human edit, upload, agent write or sidecar call
//     starts between the database snapshot and the file copy; and a window
//     that is closing waits for the requests already inside, so none is still
//     writing when the copy starts. Reads are untouched and never counted.
//     The backup and instance-hold endpoints stay open and uncounted (an
//     admin must be able to watch and resume, and a backup must not wait for
//     its own request), and so does sign-in.
//   - Inbound webhooks answer 503 while the window is open or while the
//     "webhooks" hold is set. A sender retries; nothing is accepted and then
//     silently dropped.

// quiesceOpenPrefixes are mutating routes a quiet window never holds.
var quiesceOpenPrefixes = []string{
	"/api/v1/admin/instance/backups/",
	"/api/v1/admin/instance/holds",
	"/api/v1/auth/",
	"/api/auth/",
}

// inboundWebhookPrefixes are the routes external senders call.
var inboundWebhookPrefixes = []string{
	"/api/v1/webhooks/",
	"/api/v1/page-webhooks/",
	"/api/v1/waitpoint-tokens/",
}

func isMutatingMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func hasAnyPrefix(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// quiesceGate wraps the router. Every mutating request outside the allowlist
// enters the quiet window's gate before the handler runs and leaves when the
// handler returns. GET, HEAD and OPTIONS never enter, so websockets, SSE
// streams, downloads and long polls cannot hold a drain up.
func quiesceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !isMutatingMethod(req.Method) {
			next.ServeHTTP(w, req)
			return
		}
		// Match on the cleaned path, and only let a path through the
		// allowlist when it IS its cleaned form — "/api/v1/admin/instance/
		// backups/../../x" must not slip past the hold.
		clean := path.Clean(req.URL.Path)
		canonical := clean == req.URL.Path
		if hasAnyPrefix(clean, inboundWebhookPrefixes) && quiesce.WebhooksPaused() {
			writeHeld(w, req, "Inbound webhooks are held; retry later")
			return
		}
		if canonical && hasAnyPrefix(clean, quiesceOpenPrefixes) {
			next.ServeHTTP(w, req)
			return
		}
		writer, ok := quiesce.Enter(req.Context())
		if !ok {
			writeHeld(w, req, "A consistent backup copy is being taken; writes are held for a few minutes — retry shortly")
			return
		}
		defer writer.Leave()
		next.ServeHTTP(w, req.WithContext(writer.Context()))
	})
}

func writeHeld(w http.ResponseWriter, req *http.Request, msg string) {
	retry := quiesce.Default().RetryAfter()
	if retry <= 0 {
		retry = 60 * time.Second
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(retry.Round(time.Second)/time.Second)))
	writeProblem(w, req, http.StatusServiceUnavailable, msg)
}
