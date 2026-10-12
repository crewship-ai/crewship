package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/license"
)

func TestLicenseStatus_UnlimitedCapacity(t *testing.T) {
	for _, tc := range []struct {
		name string
		lic  *license.License
	}{
		{"no license", nil}, {"community", license.New()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			NewLicenseHandler(tc.lic).Status(w, httptest.NewRequest(http.MethodGet, "/api/v1/system/license", nil))
			var got licenseResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || got.MaxCrews != 0 || got.MaxMembers != 0 || got.MaxAgents != 0 {
				t.Fatalf("expected unlimited capacity, status=%d response=%+v", w.Code, got)
			}
		})
	}
}
