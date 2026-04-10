package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/license"
)

// LicenseHandler provides the license status endpoint.
type LicenseHandler struct {
	license *license.License
}

// NewLicenseHandler creates a LicenseHandler with the given license (may be nil for community edition).
func NewLicenseHandler(lic *license.License) *LicenseHandler {
	return &LicenseHandler{license: lic}
}

type licenseResponse struct {
	Edition      string   `json:"edition"`
	LicenseID    string   `json:"license_id"`
	LicenseeOrg string   `json:"licensee_org"`
	MaxCrews     int      `json:"max_crews"`
	MaxAgents    int      `json:"max_agents_per_crew"`
	MaxMembers   int      `json:"max_members"`
	Features     []string `json:"features"`
}

// Status returns the current license edition, limits, and enabled features.
// GET /api/v1/license
func (h *LicenseHandler) Status(w http.ResponseWriter, r *http.Request) {
	if h.license == nil {
		defaults := license.CommunityDefaults()
		writeJSON(w, http.StatusOK, licenseResponse{
			Edition:    string(defaults.Edition),
			LicenseID:  defaults.LicenseID,
			MaxCrews:   defaults.MaxCrews,
			MaxAgents:  defaults.MaxAgents,
			MaxMembers: defaults.MaxMembers,
			Features:   []string{},
		})
		return
	}

	c := h.license.Claims()
	features := c.Features
	if features == nil {
		features = []string{}
	}

	writeJSON(w, http.StatusOK, licenseResponse{
		Edition:      string(c.Edition),
		LicenseID:    c.LicenseID,
		LicenseeOrg:  c.LicenseeOrg,
		MaxCrews:     c.MaxCrews,
		MaxAgents:    c.MaxAgents,
		MaxMembers:   c.MaxMembers,
		Features:     features,
	})
}
