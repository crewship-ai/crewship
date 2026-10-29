package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCraftNativeAvailabilityPageAndDefaultOff(t *testing.T) {
	f, m, ctx := craftNativeHTTPFixture(t)
	f.seedPage(t, "native-legacy", "native-legacy", "Legacy", f.userID, "")
	for _, tc := range []struct {
		slug    string
		enabled bool
	}{{"native-page", true}, {"native-legacy", false}, {"absent", false}} {
		req := httptest.NewRequest("GET", "/availability", nil).WithContext(ctx)
		req.SetPathValue("slug", tc.slug)
		w := httptest.NewRecorder()
		m.availability(w, req)
		var v map[string]bool
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != 200 || v["enabled"] != tc.enabled {
			t.Fatalf("availability %s %d %s", tc.slug, w.Code, w.Body)
		}
	}
	m.enabled = false
	req := httptest.NewRequest("GET", "/availability", nil).WithContext(ctx)
	req.SetPathValue("slug", "native-page")
	w := httptest.NewRecorder()
	m.availability(w, req)
	if !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal("default-off availability enabled")
	}
}
