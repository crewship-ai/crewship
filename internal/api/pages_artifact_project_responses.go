package api

// artifactProjectAvailabilityResponse is the body serialized by the workspace
// availability endpoint, including its explicit disabled state.
type artifactProjectAvailabilityResponse struct {
	Enabled bool `json:"enabled"`
}
