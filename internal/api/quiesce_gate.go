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
//   - While a quiet window is open every mutating request is answered 503
//     with Retry-After, so no human edit, upload, agent write or sidecar call
//     lands between the database snapshot and the file copy. Reads are
//     untouched. The backup and instance-hold endpoints stay open (an admin
//     must be able to watch and resume), and so does sign-in.
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

// quiesceGate wraps the router. It reads two in-memory flags per request and
// touches nothing else.
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
		if hasAnyPrefix(clean, inboundWebhookPrefixes) {
			if quiesce.WebhooksPaused() {
				writeHeld(w, req, "Inbound webhooks are held; retry later")
				return
			}
			if !quiesce.WritesHeld() {
				next.ServeHTTP(w, req)
				return
			}
		}
		if !quiesce.WritesHeld() {
			next.ServeHTTP(w, req)
			return
		}
		if canonical && hasAnyPrefix(clean, quiesceOpenPrefixes) {
			next.ServeHTTP(w, req)
			return
		}
		writeHeld(w, req, "A consistent backup copy is being taken; writes are held for a few minutes — retry shortly")
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
