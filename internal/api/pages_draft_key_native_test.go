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
	raw, _ := json.Marshal(map[string]string{"human": human, "operation": "check"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@"+page+"/draft-key", strings.NewReader(string(raw))).WithContext(ctx)
	r.SetPathValue("slug", "@"+page)
	w := httptest.NewRecorder()
	m.pages.LocalDraftKey(w, r)
	return w
}

func TestLocalDraftNativeCheckRequiresTheExpectedHuman(t *testing.T) {
	f, m, _ := craftNativeHTTPFixture(t)
	ctx := craftNativeGrantedWriter(t, f)
	if w := craftNativeDraftCheck(m, ctx, f.userID, "native-page"); w.Code != 403 || !strings.Contains(w.Body.String(), "draft_identity_mismatch") {
		t.Fatalf("another person's check: %d %s", w.Code, w.Body)
	}
}
