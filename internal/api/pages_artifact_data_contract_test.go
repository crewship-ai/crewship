package api

import (
	"strings"
	"testing"
)

func TestDashboardDataReadsRequireSelectedCodeVersion(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	for _, collection := range []string{"", "sales"} {
		name := "collections"
		if collection != "" {
			name = "documents"
		}
		t.Run(name, func(t *testing.T) {
			if w := dashboardRequest(t, h, ws, owner, "GET", collection, "?version=1", ""); w.Code != 200 {
				t.Fatalf("valid selected version: %d %s", w.Code, w.Body)
			}
			if w := dashboardRequest(t, h, ws, owner, "GET", collection, "", ""); w.Code != 400 {
				t.Fatalf("missing required version: %d %s", w.Code, w.Body)
			}
		})
	}
}
