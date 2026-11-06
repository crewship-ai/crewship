package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Document history of craftNativeSharedOnlyFixture: version 1 was never shared,
// version 2 is the shared pin, version 3 is the private latest.
const (
	craftNativeNeverShared   = `{"v":"never-shared-history"}`
	craftNativeSharedContent = `{"v":"shared-version"}`
	craftNativePrivateLatest = `{"v":"private-latest"}`
	craftNativeProposalID    = "private-proposal"
)

type craftNativeSharedOnly struct {
	f     *pagesTransferRig
	h     *craftNativeHTTP
	d     *craftNativeDocumentsHTTP
	owner context.Context
	token string
}

func craftNativeSharedOnlyFixture(t *testing.T) craftNativeSharedOnly {
	t.Helper()
	f, h, d, owner, token, id, body := craftNativeDocumentsFixture(t)
	craftNativeEnableVersionHistory(t, f.db, true)
	mustExec(t, f.db, `DELETE FROM artifact_version_heads WHERE page_id='native-page'`)
	mustExec(t, f.db, `DELETE FROM artifact_versions WHERE page_id='native-page'`)
	key := artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}
	store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	actor := func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }
	for base, content := range []string{craftNativeNeverShared, craftNativeSharedContent} {
		request := artifacts.VersionRequest{BaseVersion: int64(base), EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(content), MediaType: "application/json"}}}
		if _, err := store.Apply(t.Context(), key, request, actor); err != nil {
			t.Fatal(err)
		}
	}
	share := craftNativeShareBody{WorkspaceID: f.wsID, PageID: "native-page", ApplicationID: "wordcraft", EnginePin: fmt.Sprint(body["enginePin"]), SessionID: id, Generation: "1", BaseVersion: "2", TargetVersion: "2"}
	if w := craftNativeShareCall(t, d, owner, token, share); w.Code != 200 {
		t.Fatalf("share: %d %s", w.Code, w.Body)
	}
	raw := []byte(craftNativePrivateLatest)
	body["bytes"], body["baseVersion"], body["editorRevision"], body["contentHash"] = raw, "2", "10:47:9", fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if w := craftNativeDocumentCall(t, d, owner, token, "save", body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	craftNativeProposalSeed(t, f, d, "native-page", craftNativeProposalID)
	return craftNativeSharedOnly{f, h, d, owner, token}
}

// craftNativePrivatePaths names every native route that serves anything other
// than the shared preview: document bytes, version content and history metadata.
func craftNativePrivatePaths(d *craftNativeDocumentsHTTP) []struct {
	name string
	call func(t *testing.T, ctx context.Context, token string) *httptest.ResponseRecorder
} {
	type path = struct {
		name string
		call func(t *testing.T, ctx context.Context, token string) *httptest.ResponseRecorder
	}
	version := func(number string) func(*testing.T, context.Context, string) *httptest.ResponseRecorder {
		return func(_ *testing.T, ctx context.Context, token string) *httptest.ResponseRecorder {
			return craftNativeVersionReadCall(d, ctx, token, number)
		}
	}
	return []path{
		{"document", func(t *testing.T, ctx context.Context, token string) *httptest.ResponseRecorder {
			return craftNativeDocumentCall(t, d, ctx, token, "read", nil)
		}},
		{"versions list", func(_ *testing.T, ctx context.Context, token string) *httptest.ResponseRecorder {
			return craftNativeVersionsCall(d, ctx, token)
		}},
		{"never shared version", version("1")},
		{"shared version through history", version("2")},
		{"private latest version", version("3")},
		{"proposals list", func(_ *testing.T, ctx context.Context, token string) *httptest.ResponseRecorder {
			return craftNativeProposalRoute(d, ctx, token, http.MethodGet, "", "", "")
		}},
		{"proposal candidate", func(_ *testing.T, ctx context.Context, token string) *httptest.ResponseRecorder {
			return craftNativeProposalRoute(d, ctx, token, http.MethodGet, "/"+craftNativeProposalID, craftNativeProposalID, "")
		}},
	}
}

func assertCraftNativeNoPrivateContent(t *testing.T, where, body string) {
	t.Helper()
	for _, secret := range []string{craftNativeNeverShared, craftNativePrivateLatest, `{"v":"` + craftNativeProposalID + `"}`} {
		if strings.Contains(body, secret) || strings.Contains(body, base64.StdEncoding.EncodeToString([]byte(secret))) {
			t.Fatalf("%s leaked private content %s: %s", where, secret, body)
		}
	}
}

// A live Read admission never reaches anything but the shared preview, whatever
// else the same person holds short of Edit. Edit and the workspace administrator
// rule keep every path.
func TestCraftNativePrivatePathsRequireEditAuthority(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	peer := func(id, role string, levels ...string) context.Context {
		ctx := craftNativeVersionsMember(t, s.f, id, id, role, "")
		for _, level := range levels {
			mustExec(t, s.f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user',?,?,?)`, id, level, s.f.userID)
		}
		return ctx
	}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		status int
	}{
		{"read grantee", peer("shared-only-read", "MEMBER", "read"), 403},
		{"read and comment grantee", peer("shared-only-read-comment", "MEMBER", "read", "comment"), 403},
		{"workspace viewer with read", peer("shared-only-viewer", "VIEWER", "read"), 403},
		{"edit grantee", peer("shared-only-write", "MEMBER", "write"), 200},
		{"workspace administrator", peer("shared-only-admin", "ADMIN"), 200},
		{"workspace owner", peer("shared-only-owner", "OWNER"), 200},
		{"page owner", s.owner, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, _ := craftNativeIssued(t, s.h, tc.ctx)
			for _, path := range craftNativePrivatePaths(s.d) {
				t.Run(path.name, func(t *testing.T) {
					got := path.call(t, tc.ctx, token)
					if got.Code != tc.status {
						t.Fatalf("%d %s, want %d", got.Code, got.Body, tc.status)
					}
					if tc.status != 200 {
						assertCraftNativeNoPrivateContent(t, path.name, got.Body.String())
						if strings.Contains(got.Body.String(), "bytes") || strings.Contains(got.Body.String(), "createdAt") {
							t.Fatalf("refusal carried content: %s", got.Body)
						}
					}
				})
			}
			// The preview stays the shared pin for every audience, never latest.
			preview := craftNativePreviewCall(s.d, tc.ctx)
			var shared craftNativePreviewResponse
			if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &shared) != nil || shared.Version != "2" || string(shared.Bytes) != craftNativeSharedContent {
				t.Fatalf("preview: %d %s", preview.Code, preview.Body)
			}
		})
	}
}

// The refusal is a rights decision: it must not depend on the history flag, or
// a reader would see a different answer from the document read.
func TestCraftNativeReaderRefusalIsIndependentOfHistoryFlag(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	mustExec(t, s.f.db, `UPDATE feature_flags SET enabled=0 WHERE key=?`, CraftVersionHistoryFlag)
	reader := craftNativeVersionsUser(t, s.f, "shared-only-flag-read", "Reader", true, "read")
	token, _ := craftNativeIssued(t, s.h, reader)
	if w := craftNativeVersionsCall(s.d, reader, token); w.Code != 403 {
		t.Fatalf("list with the flag off: %d %s", w.Code, w.Body)
	}
	if w := craftNativeVersionReadCall(s.d, reader, token, "3"); w.Code != 403 {
		t.Fatalf("version with the flag off: %d %s", w.Code, w.Body)
	}
	if w := craftNativeVersionReadCall(s.d, s.owner, s.token, "3"); w.Code != 404 {
		t.Fatalf("the flag still governs an editor: %d %s", w.Code, w.Body)
	}
}

// Losing Edit ends private reads at once on the admission that had them and on
// any fresh admission, while the shared preview stays available.
func TestCraftNativeLostEditEndsPrivateReads(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	editor := craftNativeVersionsUser(t, s.f, "shared-only-demoted", "Editor", true, "write")
	old, _ := craftNativeIssued(t, s.h, editor)
	if w := craftNativeDocumentCall(t, s.d, editor, old, "read", nil); w.Code != 200 {
		t.Fatalf("calibration: %d %s", w.Code, w.Body)
	}
	mustExec(t, s.f.db, `UPDATE page_grants SET level='read' WHERE page_id='native-page' AND subject_id='shared-only-demoted'`)
	fresh, _ := craftNativeIssued(t, s.h, editor)
	for _, token := range []string{old, fresh} {
		for _, path := range craftNativePrivatePaths(s.d) {
			if got := path.call(t, editor, token); got.Code != 403 {
				t.Fatalf("%s after losing Edit: %d %s", path.name, got.Code, got.Body)
			}
		}
	}
	if w := craftNativePreviewCall(s.d, editor); w.Code != 200 {
		t.Fatalf("preview after losing Edit: %d %s", w.Code, w.Body)
	}
}

// On a private Page the administrator read rule is separate from write
// authority: ADMIN cannot edit, keeps the audited read, and a grant adds nothing.
func TestCraftNativePrivatePageKeepsAdministratorRead(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	admin := craftNativeVersionsMember(t, s.f, "shared-only-private-admin", "Admin", "ADMIN", "")
	reader := craftNativeVersionsUser(t, s.f, "shared-only-private-read", "Reader", true, "read")
	mustExec(t, s.f.db, `UPDATE pages SET private_audience=1 WHERE id='native-page'`)
	token, _ := craftNativeIssued(t, s.h, admin)
	for _, path := range craftNativePrivatePaths(s.d) {
		if got := path.call(t, admin, token); got.Code != 200 {
			t.Fatalf("%s for the administrator: %d %s", path.name, got.Code, got.Body)
		}
	}
	var audits int
	if err := s.f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE user_id='shared-only-private-admin'`).Scan(&audits); err != nil || audits == 0 {
		t.Fatalf("administrator reads were not audited: %d %v", audits, err)
	}
	if w := craftNativeSessionCall(s.h, reader); w.Code != 403 {
		t.Fatalf("grant widened a private Page: %d %s", w.Code, w.Body)
	}
}

// Lease status stays available to a reader as custody metadata. It carries no
// version, no history and no bytes, on the plain read and on the event stream.
func TestCraftNativeReaderLeaseStatusRevealsNoContent(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	reader := craftNativeVersionsUser(t, s.f, "shared-only-lease-read", "Reader", true, "read")
	token, _ := craftNativeIssued(t, s.h, reader)
	allowed := []string{"acquired_at", "can_edit", "can_stop_agent", "can_write", "expires_at", "generation", "handoff_requested_by", "handoff_requested_kind", "holder", "mine"}
	check := func(where string, view map[string]any) {
		t.Helper()
		keys := make([]string, 0, len(view))
		for key := range view {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != strings.Join(allowed, ",") {
			t.Fatalf("%s fields changed: %v", where, keys)
		}
		raw, _ := json.Marshal(view)
		assertCraftNativeNoPrivateContent(t, where, string(raw))
		if view["can_edit"] != false || view["can_write"] != false || view["mine"] != false {
			t.Fatalf("%s offered edit to a reader: %s", where, raw)
		}
	}
	lease := craftNativeLeaseCall(s.h, reader, token, "native-page", "read", "")
	var view map[string]any
	if lease.Code != 200 || json.Unmarshal(lease.Body.Bytes(), &view) != nil {
		t.Fatalf("lease: %d %s", lease.Code, lease.Body)
	}
	check("lease read", view)
	event, data := craftNativeOpenStream(t, s.f, s.h, reader, token)()
	if event != "lease" {
		t.Fatalf("first event %q %v", event, data)
	}
	check("event stream", data)
}
