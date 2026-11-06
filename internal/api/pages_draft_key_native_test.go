package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func craftNativeDraftCheck(m *craftNativeHTTP, ctx context.Context, human, page string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]string{"human": human, "operation": "check-native"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@"+page+"/draft-key", strings.NewReader(string(raw))).WithContext(ctx)
	r.SetPathValue("slug", "@"+page)
	w := httptest.NewRecorder()
	m.pages.LocalDraftKey(w, r)
	return w
}

// check-native is the proof a browser uses for a native draft when it has no
// stream to tell it. It must agree with native admission on every level. Comment
// visibility passes canonical Page read but is not native read.
func TestLocalDraftNativeCheckAgreesWithNativeAdmission(t *testing.T) {
	const writer = "native-scope-writer"
	for _, test := range []struct {
		name     string
		revoke   string
		readable bool
	}{
		{name: "edit", readable: true},
		{name: "read", revoke: `UPDATE page_grants SET level='read' WHERE page_id='native-page' AND subject_id='native-scope-writer'`, readable: true},
		{name: "comment only", revoke: `UPDATE page_grants SET level='comment' WHERE page_id='native-page' AND subject_id='native-scope-writer'`},
		{name: "no grant", revoke: `DELETE FROM page_grants WHERE page_id='native-page' AND subject_id='native-scope-writer'`},
		{name: "membership removed", revoke: `DELETE FROM workspace_members WHERE user_id='native-scope-writer'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m, _ := craftNativeHTTPFixture(t)
			ctx := craftNativeGrantedWriter(t, f)
			if test.revoke != "" {
				mustExec(t, f.db, test.revoke)
			}
			check := craftNativeDraftCheck(m, ctx, writer, "native-page")
			if test.readable {
				if check.Code != 200 || !strings.Contains(check.Body.String(), `"readable":true`) || check.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("readable person refused: %d %s", check.Code, check.Body)
				}
			} else if check.Code != 403 || !strings.Contains(check.Body.String(), "draft_read_revoked") {
				t.Fatalf("native read loss unproven: %d %s", check.Code, check.Body)
			}
			if admitted := craftNativeSessionCall(m, ctx).Code == 201; admitted != test.readable {
				t.Fatalf("admission %v disagrees with check %v", admitted, test.readable)
			}
		})
	}
}

// A Page without a native document keeps canonical Page read, so the check can
// never prove loss for a text draft through a native link.
func TestLocalDraftNativeCheckLeavesOtherPagesToCanonicalRead(t *testing.T) {
	f, m, _ := craftNativeHTTPFixture(t)
	ctx := craftNativeGrantedWriter(t, f)
	f.seedPage(t, "text-page", "text-page", "Text", f.userID, "")
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('text-page','user','native-scope-writer','comment',?)`, f.userID)
	if w := craftNativeDraftCheck(m, ctx, "native-scope-writer", "text-page"); w.Code != 200 {
		t.Fatalf("text page: %d %s", w.Code, w.Body)
	}
}

func TestLocalDraftNativeCheckRequiresTheExpectedHuman(t *testing.T) {
	f, m, _ := craftNativeHTTPFixture(t)
	ctx := craftNativeGrantedWriter(t, f)
	if w := craftNativeDraftCheck(m, ctx, f.userID, "native-page"); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_identity_mismatch") {
		t.Fatalf("another person's check: %d %s", w.Code, w.Body)
	}
}

// A failed ACL lookup answers false to callers who only gate reading. Erasing a
// draft needs the fault kept apart from a refusal.
func TestNativeReadWithoutCommentReportsLookupFaults(t *testing.T) {
	f, m, _ := craftNativeHTTPFixture(t)
	ctx := craftNativeGrantedWriter(t, f)
	rec, err := m.pages.loadPageForRead(ctx, f.wsID, "@native-page")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := m.pages.loadViewer(ctx, f.wsID, "native-scope-writer")
	if err != nil {
		t.Fatal(err)
	}
	viewer.Role = "VIEWER"
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.pages.nativeReadWithoutComment(canceled, f.wsID, rec, viewer); err == nil {
		t.Fatal("lookup fault reported as a plain refusal")
	}
}
