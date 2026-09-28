package pages

// CanSeePanel is the shared panel-content boundary for HTTP reads and delayed
// action execution. Page grants never widen access to another crew's data.
func CanSeePanel(role string, crewMember bool) bool {
	return role == "OWNER" || role == "ADMIN" || crewMember
}
