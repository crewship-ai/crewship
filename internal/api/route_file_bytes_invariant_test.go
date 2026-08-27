package api

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// route_file_bytes_invariant_test.go — the enumerating test #2142 asks for.
//
// #2069 taught AgentFileDownload to refuse the six generated per-agent files
// that hold resolved MCP credentials. CrewFileDownload read the identical
// storage over a second HTTP door and never got the same check (#2142) — a
// hand-written list of "the download routes" would not have caught that,
// because the second door was never ON anyone's hand-written list; it just
// existed.
//
// So this discovers the surface instead of naming it: every handler in this
// package that answers a request with a Content-Disposition header — the
// marker every byte-streaming download here sets (AgentFileDownload,
// CrewFileDownload, AttachmentHandler.Download, MemoryPortabilityHandler.Export,
// BackupHandler.Download all do, and nothing else in the package does) — is a
// route that can return file bytes. Each one found must be classified,
// exactly like route_read_scope_invariant_test.go classifies every read
// route's workspace scoping:
//
//   - fileBytesFunnelHandlers: proxies to crewshipd's ONE files/download IPC
//     endpoint (internal/server/routes_files.go handleFileDownload), which
//     itself refuses the six protected files (TestHandleFileDownload_Refuses-
//     ProtectedAgentConfig_AnySlug in that package) — so any handler landing
//     here is covered by construction, present or future, without needing to
//     replicate the check by hand on every new door.
//   - fileBytesExemptHandlers: reads bytes from somewhere else entirely, with
//     a reason a reviewer can check.
//
// A new byte-streaming handler lands in neither bucket automatically — it
// fails here until someone puts it in one, on purpose, the same way a new
// mutation route with no role or a new read route with no workspace scope
// fails its own invariant instead of shipping silently open.

// handlerFuncSig matches an HTTP handler method definition: the shape every
// handler in this package uses; see TestFileBytesScanFindsKnownHandlers for
// the check that this is still true.
var handlerFuncSig = regexp.MustCompile(`^func \(\w+ \*(\w+)\) (\w+)\(w http\.ResponseWriter, r \*http\.Request\) \{`)

// fileBytesFunnelHandlers proxy to crewshipd's single files/download IPC
// endpoint, which is where the guard lives (internal/server/routes_files.go).
// Both also carry their OWN pre-IPC check, pinned separately by
// proxy_files_scope_test.go and proxy_files_crew_door_sec_test.go — the
// funnel is the backstop, not the only place the guarantee holds.
var fileBytesFunnelHandlers = map[string]string{
	"ProxyHandler.AgentFileDownload": "proxies to crewshipd's /crews/{id}/files/download funnel; also runs " +
		"its own pre-IPC isProtectedAgentConfigPath check (#2069)",
	"ProxyHandler.CrewFileDownload": "proxies to the same funnel; also runs its own pre-IPC " +
		"IsProtectedCrewConfigPath check (#2142)",
}

// fileBytesExemptHandlers answer with file bytes from somewhere other than
// the crew/agent output tree the six protected files live in. Adding an
// entry here is a claim that has to be checked against the handler's actual
// source, not assumed from its name.
var fileBytesExemptHandlers = map[string]string{
	"AttachmentHandler.Download": "reads a content-addressed blob keyed by workspace_id+sha256 " +
		"(readAttachmentBlob, attachments.go) — never touches the crew/agent output tree at all",
	"MemoryPortabilityHandler.Export": "reads through memport.ReadSource(FormatCrewship); readCrewship " +
		"(memport/read.go) skips every source file whose extension isn't \".md\" — none of the six " +
		"protected files (.json / .toml) can ever match, regardless of which directory is read",
	"BackupHandler.Download": "requires the manage/admin role and streams a whole backup archive from an " +
		"admin-configured path (validateBackupPath) — a different subsystem entirely, not a per-file read " +
		"of the crew/agent output tree",
}

// fileBytesHandler is one handler method found to answer with
// Content-Disposition, plus enough of its source to classify it.
type fileBytesHandler struct {
	file, receiver, method string
	line                   int
}

func (h fileBytesHandler) key() string { return h.receiver + "." + h.method }

// scanFileBytesHandlers finds every handler method in this package (by
// CONTENT — filepath.Glob("*.go") over the whole package directory, not a
// naming convention) whose body sets a Content-Disposition header.
func scanFileBytesHandlers(t *testing.T) []fileBytesHandler {
	t.Helper()

	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}

	var found []fileBytesHandler
	for _, f := range all {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src := readPackageFile(t, f)
		lines := strings.Split(src, "\n")
		for i, line := range lines {
			m := handlerFuncSig.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			body, ok := extractBraceBody(lines, i)
			if !ok {
				t.Fatalf("%s:%d: could not find a matching closing brace for %s", f, i+1, line)
			}
			if strings.Contains(body, "Content-Disposition") {
				found = append(found, fileBytesHandler{file: f, receiver: m[1], method: m[2], line: i + 1})
			}
		}
	}
	return found
}

// extractBraceBody returns the source text from the opening line (index
// start, whose own brace-count is included) up to and including the line
// that closes it, by counting braces character by character. Go source
// inside this package does not put unbalanced braces in string literals or
// comments in a way that would defeat this within a handler body — the same
// assumption route_authz_invariant_test.go's neighbours make about simpler
// line-oriented scans, traded here for something that cannot bleed into the
// NEXT function the way a fixed-line lookahead can (see
// readRouteWrapperLookahead's own note on that failure mode).
func extractBraceBody(lines []string, start int) (string, bool) {
	depth := 0
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		line := lines[i]
		b.WriteString(line)
		b.WriteByte('\n')
		for _, r := range line {
			switch r {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth <= 0 && i > start {
			return b.String(), true
		}
		// The signature line itself opens at least one brace; if depth already
		// dropped back to 0 on that same line (a one-line body, which does not
		// occur for these handlers but should not hang if it ever did) treat it
		// as closed too.
		if i == start && depth <= 0 {
			return b.String(), true
		}
	}
	return "", false
}

// TestEveryFileBytesHandlerIsClassified is the invariant: a handler that
// streams file bytes either proxies to the guarded crewshipd funnel or is
// explicitly excused above. A new one that is neither fails here.
func TestEveryFileBytesHandlerIsClassified(t *testing.T) {
	handlers := scanFileBytesHandlers(t)

	var unclassified []string
	for _, h := range handlers {
		key := h.key()
		_, funnel := fileBytesFunnelHandlers[key]
		_, exempt := fileBytesExemptHandlers[key]
		if !funnel && !exempt {
			unclassified = append(unclassified, formatOffender(h.file, h.line, key))
		}
	}

	if len(unclassified) > 0 {
		t.Fatalf(`%d handler(s) answer with Content-Disposition (they can return file bytes) but are neither
known to proxy through crewshipd's guarded files/download funnel nor explicitly excused:
%s

Fix one of these ways:
  - if it proxies to /crews/{id}/files/download, add it to fileBytesFunnelHandlers
    in this file, or
  - if it reads bytes from somewhere else, add it to fileBytesExemptHandlers WITH
    a reason a reviewer can check against its actual source.
Do not add it to either map to make this test pass without reading the handler —
the maps are a review record, and #2142 was exactly a route nobody's list named.`,
			len(unclassified), strings.Join(unclassified, "\n"))
	}
}

// TestFileBytesScanFindsKnownHandlers guards the guard: if handlerFuncSig (or
// the Content-Disposition marker convention) stops matching, the test above
// passes vacuously. Pins the five handlers known to answer with file bytes on
// the tree this landed against, and that every classified entry still
// corresponds to a real, still-Content-Disposition-setting handler.
func TestFileBytesScanFindsKnownHandlers(t *testing.T) {
	handlers := scanFileBytesHandlers(t)

	const minHandlers = 5
	if len(handlers) < minHandlers {
		t.Fatalf("scanned only %d Content-Disposition handler(s), expected at least %d — "+
			"handlerFuncSig or the Content-Disposition convention has likely stopped matching, "+
			"which would make TestEveryFileBytesHandlerIsClassified pass vacuously", len(handlers), minHandlers)
	}

	live := make(map[string]bool, len(handlers))
	for _, h := range handlers {
		live[h.key()] = true
	}

	for _, want := range []string{
		"ProxyHandler.AgentFileDownload",
		"ProxyHandler.CrewFileDownload",
		"AttachmentHandler.Download",
		"MemoryPortabilityHandler.Export",
		"BackupHandler.Download",
	} {
		if !live[want] {
			t.Errorf("expected the scan to find %s (a known Content-Disposition handler) but it did not", want)
		}
	}

	for key := range fileBytesFunnelHandlers {
		if !live[key] {
			t.Errorf("fileBytesFunnelHandlers has a stale entry %q — no such handler sets Content-Disposition; remove it", key)
		}
	}
	for key := range fileBytesExemptHandlers {
		if !live[key] {
			t.Errorf("fileBytesExemptHandlers has a stale entry %q — no such handler sets Content-Disposition; remove it", key)
		}
	}
	for key, reason := range fileBytesFunnelHandlers {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("fileBytesFunnelHandlers[%q] has no reason", key)
		}
	}
	for key, reason := range fileBytesExemptHandlers {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("fileBytesExemptHandlers[%q] has no reason", key)
		}
	}
}
